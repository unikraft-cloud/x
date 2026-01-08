# `oidext`

`oidext` is a small Go module for **mapping Go structs to ASN.1 OID-based X.509 extensions**, and back again.

It is designed for use with **X.509 certificates and certificate signing requests (CSRs)** where additional, structured metadata must be embedded as **custom extensions**, such as:

- Host or workload attributes
- Platform or attestation metadata
- Licensing or entitlement claims
- Infrastructure fingerprints

Each exported struct field is encoded as a **distinct ASN.1 DER value**, stored under its own OID, derived from a **caller-supplied prefix OID** and a **field-specific suffix**.


## Design goals

- **Deterministic and explicit**
  Every field maps to a clearly defined OID; no implicit or heuristic behavior.

- **CSR- and certificate-friendly**
  Produces `pkix.Extension` values that can be directly attached to:
  - `x509.CertificateRequest.Extensions`
  - `x509.Certificate.ExtraExtensions`

- **Strong typing**
  Uses Go’s `encoding/asn1` directly - with additional support for floating points.

- **Prefix-based OID namespacing**
  All extensions are derived from a single prefix OID (e.g. an enterprise or product OID).

- **Round-trip safe**
  Encode → decode produces the original struct values.

- **Minimal dependencies**
  Standard library only.


## Conceptual model

Given:

- A **prefix OID** (e.g. `1.3.6.1.4.1.55555.1`)
- A Go struct with tagged fields

```go
type HostAttributes struct {
  Hostname    string `oid:"1,critical"`
  Fingerprint []byte `oid:"2,omitempty"`
  Cpus        int    `oid:"3"`
}

const prefix = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 55555, 1}

attrs := HostAttributes{
  Hostname:    "node0-1",
  Fingerprint: []byte{0xde, 0xad, 0xb0, 0xb0}
}

// To encode:
exts, err := oidext.Encode(prefix, attrs)
if err != nil {
  log.Fatal(err)
}

// Attach to a CSR or certificate:
csr.ExtraExtensions = append(csr.ExtraExtensions, exts...)

// Later, decoding:
exts, err = oidext.Decode(prefix, csr.Extensions, &attrs)
if err != nil {
  log.Fatal(err)
}
```
