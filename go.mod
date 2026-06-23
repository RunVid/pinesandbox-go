// Vanity import path (COMPUTER_GO_SDK_DESIGN.md §2) — decoupled from the GitHub org;
// the backing repo is served via a go-import meta tag. In-monorepo dev uses this path
// directly (no go.work; standalone module).
module go.pinesandbox.io/computer

// 1.23 for range-over-func (iter.Seq) — the SSE iterator shape (§8). The toolchain may
// be newer; this is the minimum the module needs.
go 1.23

require github.com/cloudflare/circl v1.6.3

require (
	golang.org/x/crypto v0.30.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
)
