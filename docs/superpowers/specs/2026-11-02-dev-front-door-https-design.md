# HTTPS dev front door

`gsx dev` currently reads the host and port from `VITE_DEV_URL` but reconstructs
the origin with `http://`. This prevents a Vite HTTPS front door from working
with GSX control requests and advertises an insecure origin to the Go child.
Browsers require HTTPS for camera and microphone access on LAN hostnames.

The existing `VITE_DEV_URL` is the public origin. Preserve its HTTP or HTTPS
scheme, keeping the established host override and port precedence. Parse it
as an absolute HTTP(S) origin; reject credentials, non-root paths, query strings,
fragments and malformed URLs. Use standard URL and host/port formatting so IPv6
works. An absent URL retains the current HTTP default and port allocation.

Vite owns TLS termination. Its native `server.https` accepts certificates from
mkcert or another trusted development CA, and the GSX plugin already supports
that server configuration. Do not add a second certificate configuration API,
certificate generator, proxy, or TLS-verification bypass to GSX. The backend
upstream can remain HTTP on loopback, independently of the public origin.

GSX management HTTP clients retain normal certificate-chain and hostname
verification. Real TLS tests must prove accepted trusted certificates and
rejected unknown authorities or incorrect hostnames. Control endpoints,
including reload, events, commands and managed-front-door verification, must
work with the resolved HTTPS origin.

The generated scaffold must print the resolved public URL. Document native
Vite TLS configuration and the requirement to install the development CA in
each client machine's trust store. Never commit developer private keys.

Acceptance includes the ordinary HTTP tests, HTTPS URL/port/host cases, real
TLS requests, the plugin's HTTPS/WSS endpoints, and a consumer application
served through the trusted HTTPS front door. No camera/microphone access is
needed to test transport support.
