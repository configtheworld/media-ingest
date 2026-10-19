# Reverse proxy and authentication

In the default `AUTH_MODE=proxy`, an authenticating reverse proxy (Authelia, Authentik, oauth2-proxy, …)
signs users in and passes the user name in `Remote-User`. media-ingest trusts that header **only**
from `TRUSTED_PROXIES` and fails closed otherwise.

Three rules for every setup:

1. **The proxy must overwrite `Remote-User` and `Remote-Groups`.** A client must never be able to send
   its own. The examples below do this.
2. **Only the proxy may reach media-ingest.** Don't expose port 8097 elsewhere (see
   [deployment](deployment.md#checklist)).
3. **Large uploads.** Uploads are 8 MiB chunks. Allow request bodies of at least 16 MiB, don't buffer
   them, and keep read timeouts long enough for 8 MiB on your slowest uplink (60 s ≈ 1.1 Mbit/s).

Examples use `media.example.com`, Authelia at `authelia:9091` and media-ingest at `media-ingest:8097`.
The Authelia endpoints are the ones its documentation lists for each proxy
(`/api/authz/forward-auth`, `/api/authz/auth-request`).

## Authelia access rules

The web app manifest and its icons are fetched by browsers without cookies. Let them through so the
app can be installed to the home screen (they are static and contain nothing private):

```yaml
access_control:
  rules:
    - domain: media.example.com
      resources: ['^/manifest\.webmanifest$', '^/icons/']
      policy: bypass
    - domain: media.example.com
      policy: two_factor
```

## Traefik (v3) with Authelia forwardAuth

Dynamic configuration (file provider):

```yaml
http:
  middlewares:
    authelia:
      forwardAuth:
        address: http://authelia:9091/api/authz/forward-auth
        trustForwardHeader: true
        authResponseHeaders: [Remote-User, Remote-Groups, Remote-Email, Remote-Name]
  routers:
    media-ingest:
      rule: Host(`media.example.com`)
      entryPoints: [websecure]
      middlewares: [authelia]
      service: media-ingest
      tls:
        certResolver: letsencrypt
  services:
    media-ingest:
      loadBalancer:
        servers:
          - url: http://media-ingest:8097
```

`authResponseHeaders` replaces any client-supplied value of those headers. Traefik has no body size
limit by default, but **v3 sets `readTimeout` to 60 s** on entry points. For slow uplinks raise it in the
static configuration:

```yaml
entryPoints:
  websecure:
    address: ":443"
    transport:
      respondingTimeouts:
        readTimeout: 600s
```

With Docker labels instead of a file, use the same values, e.g.
`traefik.http.routers.media-ingest.middlewares=authelia@docker`. Set `TRUSTED_PROXIES` to the address
the Traefik container connects from (the Docker network's subnet, or the proxy host's IP).

## Nginx with Authelia (auth_request)

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name media.example.com;
    # ssl_certificate … ;

    client_max_body_size 64m;        # > one 8 MiB chunk
    proxy_request_buffering off;     # stream chunks straight to the app
    proxy_read_timeout 600s;
    proxy_send_timeout 600s;

    location /internal/authelia/authz {
        internal;
        proxy_pass http://authelia:9091/api/authz/auth-request;
        proxy_set_header X-Original-Method $request_method;
        proxy_set_header X-Original-URL $scheme://$http_host$request_uri;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header Content-Length "";
        proxy_set_header Connection "";
        proxy_pass_request_body off;
    }

    location / {
        auth_request /internal/authelia/authz;
        auth_request_set $user $upstream_http_remote_user;
        auth_request_set $groups $upstream_http_remote_groups;
        auth_request_set $redirection_url $upstream_http_location;
        error_page 401 =302 $redirection_url;

        proxy_set_header Remote-User $user;      # overwrites anything the client sent
        proxy_set_header Remote-Groups $groups;
        proxy_set_header Host $host;             # needed for the same-origin check
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_pass http://media-ingest:8097;
    }

    location ~ ^/(manifest\.webmanifest|icons/) {
        proxy_set_header Remote-User "";
        proxy_pass http://media-ingest:8097;      # public static files
    }
}
```

## Caddy with Authelia (forward_auth)

```caddyfile
media.example.com {
	@public path /manifest.webmanifest /icons/*
	handle @public {
		request_header -Remote-User
		request_header -Remote-Groups
		reverse_proxy media-ingest:8097
	}
	handle {
		# route keeps this order: drop client-supplied identity headers,
		# authenticate, then proxy.
		route {
			request_header -Remote-User
			request_header -Remote-Groups
			forward_auth authelia:9091 {
				uri /api/authz/forward-auth
				copy_headers Remote-User Remote-Groups Remote-Name Remote-Email
			}
			reverse_proxy media-ingest:8097
		}
	}
}
```

Caddy has no request body limit and no read timeout for proxied requests by default.

## Cloudflare and tunnels

- With the Cloudflare proxy (orange cloud) a single request is limited to 100 MB on the Free plan; the
  8 MiB chunks stay well below it.
- Through WireGuard or similar tunnels, requests may arrive from the tunnel's address; that address is
  what goes into `TRUSTED_PROXIES`. Make sure nothing else can reach port 8097 through the tunnel.

## Without a proxy

- `AUTH_MODE=basic`: built-in sign-in with an argon2id password hash and server-side sessions. Put TLS in
  front (any proxy without auth, or a TLS terminator).
- `AUTH_MODE=none` + `AUTH_NONE_CONFIRM=true`: no authentication at all. Only for a port bound to
  `127.0.0.1` on a single-user machine.

## Checking it

```sh
# From the proxy host, through the proxy: should show the app (after signing in).
# Directly, from anywhere else: must be 401.
curl -s -o /dev/null -w '%{http_code}\n' -H 'Remote-User: admin' http://media-ingest-host:8097/api/session
```
