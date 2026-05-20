# Runner: CUDA driver mismatch + Keylase patch

Operator-side recipe for a class of failure that looks like a gateway
bug but lives on the orchestrator's GPU host. Captures the full
diagnosis path and the fix sequence — including the Keylase NVENC
session-limit patch you almost always want alongside it.

---

## Symptoms

From the gateway's point of view:
- `POST /v1/abr` succeeds, broker returns `2xx`, reservation commits.
- `GET /v1/abr/{work_id}` reports `status: "processing"` indefinitely.
- No `master.m3u8` ever lands in RustFS under `abr-out/<api_key_id>/<work_id>/`.
- The portal Playground spins on `Transcode` forever.

From the runner's host (orchestrator side):
```
[job abr-…] phase: downloading
[job abr-…] phase: probing
[job abr-…] phase: encoding
[job abr-…] error (ENCODE_ERROR): ffmpeg encode 1080p: exit status 187
…
[AVHWDeviceContext] cu->cuInit(0) failed
  → CUDA_ERROR_SYSTEM_DRIVER_MISMATCH:
    system has unsupported display driver / cuda driver combination
Device creation failed: -542398533
```

The gateway never sees the error because the abr-runner is async over
the capability-broker and the broker doesn't pass-through the runner's
`/v1/video/transcode/abr/status` endpoint. Until the gateway grows a
webhook-callback path (see
[`docs/exec-plans/tech-debt-tracker.md`](../exec-plans/tech-debt-tracker.md)),
runner errors are invisible to us.

---

## Diagnosis

`CUDA_ERROR_SYSTEM_DRIVER_MISMATCH` means the user-space CUDA libs
inside the runner container want a newer NVIDIA driver than the host
kernel module provides. The GPU itself is fine — `nvidia-smi` will
work — but FFmpeg's `h264_nvenc` / `*_cuvid` paths refuse to initialise.

### Compare both sides

```bash
# Host driver + CUDA cap on the orchestrator host
nvidia-smi | head -2

# CUDA toolkit version baked into the runner image
docker exec <runner-container> sh -c \
  'cat /usr/local/cuda/version.txt 2>/dev/null || nvcc --version'
```

The runner's CUDA major.minor must be **≤** the host driver's
advertised "CUDA Version" column.

### NVIDIA driver → max CUDA mapping

| NVIDIA driver branch | Max CUDA toolkit |
|---|---|
| 550.x | 12.4 |
| 555.x | 12.5 |
| 560.x | 12.6 |
| 565.x | 12.7 |
| 570.x | 12.8 |
| 575.x | 12.9 |
| 580.x | 13.0 |

Host driver < image CUDA → `cuInit(0)` fails immediately.

---

## Fix A — pin the runner image to the host's CUDA

Operationally safer: keep the host driver where it is, rebuild the
runner image against a CUDA version the host already supports.

In the runner's Dockerfile at
`~/git-repos/livepeer-cloud-spe/livepeer-modules-transcode-runners`:

```dockerfile
# Match the host's max CUDA. For a driver 550.x host this means 12.4.x.
FROM nvidia/cuda:12.4.1-devel-ubuntu24.04 AS build
…
FROM nvidia/cuda:12.4.1-runtime-ubuntu24.04 AS runtime
```

Rebuild, push, redeploy the runner. Zero host changes.

---

## Fix B — upgrade the host driver to match the runner

Use this when you need a newer driver anyway (e.g. CUDA features the
runner uses, or you're going to apply the Keylase patch which requires
a specific driver minor version).

For an RTX 20-series / Turing card on Ubuntu 22.04+:

```bash
# 1. See what's available
apt-cache search nvidia-driver-5
apt-cache madison nvidia-headless-580

# 2. Install the headless 580 driver (no X, just the kernel module + NVENC libs)
#    libnvidia-encode-580 is NOT pulled by the nvidia-headless-580 metapackage
#    on 580+ — install it explicitly or h264_nvenc won't open.
sudo apt install \
  nvidia-headless-580 \
  nvidia-utils-580 \
  libnvidia-encode-580

sudo reboot
```

After reboot:
```bash
nvidia-smi | head -2
# expect: Driver Version: 580.x   CUDA Version: 13.0 (or 12.9)
```

### Variant: pinning a specific minor version for Keylase

The Keylase NVENC patch (next section) is keyed off the **exact** driver
minor version. If `apt` just installed `580.159.04` but Keylase only
ships an offset for `580.159.03`, downgrade explicitly:

```bash
# Confirm .03 is still in the repo
apt-cache madison libnvidia-encode-580 | grep '580.159.03'

# Downgrade every 580 package to .03 in one transaction
sudo apt install \
  nvidia-headless-580=580.159.03-1ubuntu1 \
  nvidia-headless-no-dkms-580=580.159.03-1ubuntu1 \
  nvidia-utils-580=580.159.03-1ubuntu1 \
  libnvidia-cfg1-580=580.159.03-1ubuntu1 \
  libnvidia-compute-580=580.159.03-1ubuntu1 \
  libnvidia-decode-580=580.159.03-1ubuntu1 \
  libnvidia-encode-580=580.159.03-1ubuntu1 \
  libnvidia-gpucomp-580=580.159.03-1ubuntu1 \
  nvidia-dkms-580=580.159.03-1ubuntu1 \
  nvidia-firmware-580=580.159.03-1ubuntu1 \
  nvidia-kernel-common-580=580.159.03-1ubuntu1 \
  nvidia-kernel-source-580=580.159.03-1ubuntu1

# Freeze so unattended-upgrades / `apt upgrade` doesn't roll forward
sudo apt-mark hold \
  nvidia-headless-580 nvidia-headless-no-dkms-580 nvidia-utils-580 \
  libnvidia-cfg1-580 libnvidia-compute-580 libnvidia-decode-580 \
  libnvidia-encode-580 libnvidia-gpucomp-580 nvidia-dkms-580 \
  nvidia-firmware-580 nvidia-kernel-common-580 nvidia-kernel-source-580
```

If `apt-cache madison` no longer lists the version you need, NVIDIA has
retired it from the repo — fall back to the `.run` installer from
`https://download.nvidia.com/XFree86/Linux-x86_64/<version>/`.

---

## Keylase NVENC session-limit patch

Consumer GeForce cards (RTX 2080 included) ship with a driver-level
cap on concurrent NVENC sessions: 3 on older drivers, 8 on recent ones.
Once you queue more than the cap in parallel, every additional `ffmpeg`
that opens NVENC fails with `OpenEncodeSessionEx failed`. The runner's
`max_concurrent_sessions` config doesn't override this — the cap is in
`libnvidia-encode.so`.

[Keylase nvidia-patch](https://github.com/keylase/nvidia-patch) binary-
patches the relevant `libnvidia-encode.so.<version>` to remove the cap.
The patch is driver-version-specific (the offsets change between minor
releases), which is why you sometimes need to pin to a specific driver
version (see Fix B's variant above).

### Apply

```bash
git clone https://github.com/keylase/nvidia-patch.git
cd nvidia-patch

# Confirm the version you have is supported. Required.
grep "$(nvidia-smi --query-gpu=driver_version --format=csv,noheader)" patch-list.json
# Should print one or more matching lines. If empty, you need to either:
#   - install a supported driver minor version (Fix B variant), or
#   - wait for Keylase to add an offset, or
#   - submit a PR (not hard — see CONTRIBUTING.md in the patch repo).

sudo bash patch.sh
# Optional (frame-buffer capture, NOT needed for transcoding):
# sudo bash patch-fbc.sh

# patch.sh makes a backup at /opt/nvidia/libnvidia-encode-backup/
# and writes the patched .so in-place at:
# /usr/lib/x86_64-linux-gnu/libnvidia-encode.so.<version>
```

### Verify

```bash
# Spin up many parallel encodes — stock cap is 3 or 8; patched allows N.
for i in 1 2 3 4 5 6 7 8 9 10; do
  ffmpeg -hide_banner -loglevel error \
    -f lavfi -i testsrc2=size=320x240:rate=30 -t 30 \
    -c:v h264_nvenc -f null - >/dev/null 2>&1 &
done
sleep 3
nvidia-smi --query-gpu=encoder.stats.sessionCount --format=csv
wait
```

Patched 2080 will report `10`. Stock 2080 caps at 8 with a recent
driver (3 with very old ones).

### Operational notes

- **Re-apply after every driver bump.** A routine `apt upgrade` that
  reinstalls `libnvidia-encode-*` wipes the patch. Use `apt-mark hold`
  on the nvidia-* packages (Fix B variant above) to make this a
  deliberate event, not a surprise.
- **DKMS / kernel updates are safe.** Keylase patches user-space libs
  only; the kernel module is untouched.
- **Monitor in production.** Scrape
  `nvidia-smi --query-gpu=encoder.stats.sessionCount,utilization.encoder --format=csv`
  and alert if `sessionCount` hits the unpatched cap (3 or 8). That's
  the signature of an accidentally-replaced lib.

---

## End-to-end smoke after the fix

Always validate before sending a real job through the gateway:

```bash
# Driver healthy
nvidia-smi

# NVENC + NVDEC libs visible to the loader
ldconfig -p | grep -E 'libnvidia-encode|libnvcuvid'

# Hardware encode actually runs (no CUDA_ERROR_*)
docker exec -it <runner-container> ffmpeg -hide_banner \
  -f lavfi -i testsrc2=duration=2:size=1280x720:rate=30 \
  -c:v h264_nvenc -f null -
```

If that completes cleanly, queue a real `/v1/abr` job from the
playground. The runner will transcode and PUT outputs back to RustFS;
the playground polling loop will flip `status` to `succeeded` and the
Play button lights up.

---

## Cross-references

- [`../design-docs/abr-pipeline.md`](../design-docs/abr-pipeline.md) — the gateway-side flow this guide complements
- [`../exec-plans/tech-debt-tracker.md`](../exec-plans/tech-debt-tracker.md) — webhook-callback work that would let the gateway *know* about runner-side errors instead of waiting for HEAD-probe timeouts
- [`../../DEPLOYMENT.md`](../../DEPLOYMENT.md#common-failure-modes) — operator runbook this entry plugs into
