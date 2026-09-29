#!/usr/bin/env python3
"""Verify gateway wire/journal roundtrips using LOC's real cryptographic verifier.

Run with the sibling LOC venv (see DEPLOYMENT.md). --generate intentionally
refreshes checked-in fixtures; ordinary runs verify them without rewriting.
Only public deterministic test keys are used. No services or funds are accessed.
"""

# Imports follow explicit sibling-checkout path selection.
# ruff: noqa: E402

import argparse
import base64
import dataclasses
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument(
    "--loc-repo", type=Path, default=ROOT.parent / "livepeer-modules-open-clearinghouse"
)
parser.add_argument("--generate", action="store_true")
args = parser.parse_args()
loc = args.loc_repo.resolve()
sys.path[:0] = [
    str(loc),
    str(loc / "src"),
    str(loc / "src/livepeer_open_clearinghouse/_gen"),
]
from eth_keys.datatypes import PrivateKey, Signature
from eth_hash.auto import keccak
from livepeer.payments.v1 import types_pb2 as pb
from tests.fixtures.signed_settlement import (
    signed_job_settlement,
    signed_session_settlement,
    delegated_key,
)
from livepeer_open_clearinghouse.providers.settlement_verification import (
    JobSettlementExpectation,
    SessionSettlementExpectation,
    SettlementVerificationError,
    verify_job_settlement,
    verify_session_settlement,
)

fixture_path = ROOT / "gateway/internal/server/testdata/shared-wallet-v3.json"
caller = PrivateKey(bytes.fromhex("00" * 31 + "01"))
payer = PrivateKey(b"\x02" * 32)
domain = "0x" + "aa" * 32
gateway_id = "11111111-1111-4111-8111-111111111111"


def sign(key, digest):
    value = bytearray(key.sign_msg_hash(digest).to_bytes())
    value[64] += 27
    return bytes(value)


def personal(digest):
    return keccak(b"\x19Ethereum Signed Message:\n32" + digest)


def generate():
    cases = []
    for account in ("loc-prod-test", "loc-dev-test"):
        for kind in ("job", "session", "refill", "refill_refused"):
            work = account + "-" + kind
            unit = "video-frame-megapixel" if kind == "job" else "output_seconds"
            capability = (
                "video:transcode.abr" if kind == "job" else "video:transcode.live"
            )
            payload = pb.SpendAuthorizationPayload(
                domain="livepeer-spend-authorization/v3",
                wholesale_account_id=account,
                payer=payer.public_key.to_canonical_address(),
                payee=b"\x11" * 20,
                authorization_id=work,
                request_id=work + "-request",
                session_id="" if kind == "job" else gateway_id,
                protocol="paid-job/v1" if kind == "job" else "paid-session/v1",
                capability=capability,
                offering="test",
                max_total_units=100,
                max_debit_wei=pb.BigUInt(value=b"\x64"),
                not_before="2026-08-20T00:00:00Z",
                expires_at="2026-08-21T00:00:00Z",
                request_digest=hashlib.sha256(b"{}").digest(),
                caller_public_key=caller.public_key.to_compressed_bytes(),
                revision=1 if kind in ("refill", "refill_refused") else 0,
                predecessor_authorization_id=account + "-session"
                if kind in ("refill", "refill_refused")
                else "",
                broker_uri="https://broker.example.invalid",
                chain_id=42161,
                denomination="wei",
                settlement_domain_id=domain,
                accepted_price=pb.AcceptedPrice(
                    price_per_unit_wei=pb.BigUInt(value=b"\x01"),
                    units_per_price=1,
                    work_unit_name=unit,
                    capability=capability,
                    offering="test",
                    quote_ref=pb.QuoteRef(
                        quote_id="q-1",
                        quote_version=1,
                        constraint_fingerprint=b"\0" * 32,
                        route_fingerprint=b"\x11" * 32,
                    ),
                ),
            )
            wire_payload = payload.SerializeToString(deterministic=True)
            auth = pb.SpendAuthorization(
                payload=payload, signature=sign(payer, personal(keccak(wire_payload)))
            )
            wire = auth.SerializeToString(deterministic=True)
            common = dict(
                work_id=work,
                work_unit=unit,
                amount_wei=1,
                per_units=1,
                wholesale_account_id=account,
                authorization_id=work,
                authorized_value_wei=100,
                reserved_value_wei=100,
                released_value_wei=69,
            )
            if kind == "job":
                evidence = signed_job_settlement(
                    account_version=9007199254740993,
                    job_id="broker-job",
                    request_id=work + "-request",
                    actual_units=31,
                    **common,
                )
            else:
                if kind == "refill_refused":
                    common["work_id"] = common["authorization_id"] = (
                        account + "-session"
                    )
                evidence = signed_session_settlement(
                    gateway_session_id=gateway_id,
                    debited_units=31,
                    billed_value_wei=31,
                    settlement_seq=2 if kind in ("refill", "refill_refused") else 1,
                    **common,
                )
            proof = sign(
                caller, personal(keccak(b"livepeer-invocation-proof/v1\0" + wire))
            )
            cases.append(
                dict(
                    name=work,
                    kind=kind,
                    account=account,
                    work_id=work,
                    request_id=work + "-request",
                    unit=unit,
                    authorization=base64.b64encode(wire).decode(),
                    caller_proof=base64.b64encode(proof).decode(),
                    settlement=evidence,
                )
            )
    return cases


def verify(case):
    wire = base64.b64decode(case["authorization"], validate=True)
    auth = pb.SpendAuthorization.FromString(wire)
    p = auth.payload
    assert (
        p.domain == "livepeer-spend-authorization/v3"
        and p.wholesale_account_id == case["account"]
    )
    sig = bytearray(auth.signature)
    sig[64] -= 27
    assert (
        Signature(bytes(sig))
        .recover_public_key_from_msg_hash(
            personal(keccak(p.SerializeToString(deterministic=True)))
        )
        .to_canonical_address()
        == p.payer
    )
    proof = bytearray(base64.b64decode(case["caller_proof"], validate=True))
    proof[64] -= 27
    assert (
        Signature(bytes(proof))
        .recover_public_key_from_msg_hash(
            personal(keccak(b"livepeer-invocation-proof/v1\0" + wire))
        )
        .to_compressed_bytes()
        == p.caller_public_key
    )
    common = dict(
        wholesale_account_id=case["account"],
        settlement_domain_id=domain,
        work_id=case["settlement"]["payload"]["work_id"],
        work_unit=case["unit"],
        amount_wei=1,
        per_units=1,
        quote_id="q-1",
        quote_version=1,
        constraint_fingerprint=b"\0" * 32,
        route_fingerprint=b"\x11" * 32,
        funded_value_wei=100,
        authorization_id=case["settlement"]["payload"]["authorization_id"],
        authorized_value_wei=100,
    )
    if case["kind"] == "job":
        expected = JobSettlementExpectation(
            request_id=case["request_id"],
            job_id="broker-job",
            actual_units=31,
            max_total_units=100,
            **common,
        )
        verifier = verify_job_settlement
    else:
        expected = SessionSettlementExpectation(
            gateway_session_id=gateway_id,
            broker_session_id="broker-session-1",
            predecessor_work_id="",
            rotation_generation=0,
            last_settlement_seq=0,
            **common,
        )
        verifier = verify_session_settlement
    verifier(case["settlement"], settlement_keys=[delegated_key()], expected=expected)
    try:
        verifier(
            case["settlement"],
            settlement_keys=[delegated_key()],
            expected=dataclasses.replace(
                expected, wholesale_account_id="wrong-account"
            ),
        )
    except SettlementVerificationError as exc:
        assert exc.code == "wholesale_account_mismatch", exc
    else:
        raise AssertionError("LOC accepted evidence for another account")


if args.generate:
    cases = generate()
    for case in cases:
        verify(case)
    fixture_path.write_text(json.dumps(cases, indent=2) + "\n")
cases = json.loads(fixture_path.read_text())
for case in cases:
    verify(case)
with tempfile.TemporaryDirectory(prefix="gateway-v3-") as temp:
    output = Path(temp) / "roundtrip.json"
    subprocess.run(
        [
            "go",
            "-C",
            str(ROOT / "gateway"),
            "test",
            "-count=1",
            "./internal/server",
            "-run",
            "^TestSharedWalletV3JournalAndWire$",
        ],
        env={**os.environ, "SHARED_WALLET_ROUNDTRIP": str(output)},
        check=True,
    )
    roundtrips = json.loads(output.read_text())
    assert len(roundtrips) == len(cases)
    for case in roundtrips:
        verify(case)
print(
    f"PASS: {len(cases)} v3 ABR/live/refill/refused-successor fixtures, caller proofs, encrypted restart, LOC signatures and wrong-account rejection"
)
