# troubleshooting

Operator-facing recipes for failures the gateway can't fix itself.

Each entry follows the same shape: **symptom → diagnosis → fix**.

| Guide | When it bites |
|---|---|
| [`runner-cuda-driver-mismatch.md`](./runner-cuda-driver-mismatch.md) | Runner logs `CUDA_ERROR_SYSTEM_DRIVER_MISMATCH` on its host, gateway shows jobs stuck in `processing` because the runner errors out before writing anything to RustFS. Covers the driver/CUDA alignment fix and the Keylase NVENC session-limit patch. |
| [`payer-daemon-stale-ticket-params.md`](./payer-daemon-stale-ticket-params.md) | Receiver-side payment-daemon rejects our minted tickets mid-session with `validator: invalid recipientRand for recipientRandHash`, runner never starts work because EV credit stays 0. Caused by our payer-daemon caching `TicketParams` while the receiver rotated state. Fix: `stop` + `rm -f` + `up` the payer-daemon to flush its cache. |

When a real-world incident has a non-obvious root cause you'd want a future operator to skip the discovery cost on — write it up here. The harness rule applies: if it isn't checked in, the next person doesn't know.
