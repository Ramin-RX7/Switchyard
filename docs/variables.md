# Variables

Variables are nginx-style `$name` placeholders that resolve to values derived from the incoming request. They are used in:

- [`set_headers`](set-headers.md) value templates — e.g. `"X-Real-IP": "$remote_addr"`
- [`set_response_headers`](set-headers.md#response-headers-set_response_headers) value templates — e.g. `"X-Request-Scheme": "$scheme"`
- [Log format](logging.md) strings via the `{var.NAME}` field — e.g. `{var.remote_addr}`
- [`response` location](routing.md) bodies and headers — e.g. `"body": "{\"time\":\"$time_iso8601\"}"`
- The `backend_error`, `not_found`, and `overflow` [response](config-reference.md#response) bodies and headers

All variables are resolved against the [request snapshot](concepts.md#request-snapshot) captured at the start of the request, before any forwarding or header mutation. Changes made by `set_headers` do not affect variable resolution. The `time_iso8601` and `time_unix` variables resolve from the snapshot's receipt time.

---

## Available Variables

| Variable | Description | Example value |
|----------|-------------|---------------|
| `remote_addr` | Client IP address (host part only, port excluded) | `203.0.113.5` |
| `remote_port` | Client port | `54321` |
| `host` | Value of the `Host` request header | `api.example.com` |
| `scheme` | Request scheme, detected from TLS presence | `http` or `https` |
| `request_method` | HTTP method | `GET` |
| `request_uri` | Full path including query string | `/v1/users?id=1&sort=asc` |
| `uri` | Path only, no query string | `/v1/users` |
| `args` | Raw query string (alias for `query_string`) | `id=1&sort=asc` |
| `query_string` | Raw query string (alias for `args`) | `id=1&sort=asc` |
| `time_iso8601` | Request-receipt time, RFC 3339 | `2026-01-02T15:04:05Z07:00` |
| `time_unix` | Request-receipt time, Unix seconds | `1735830245` |
| `http_<name>` | Value of any request header | see below |

### `http_<name>` — Arbitrary Request Headers

Prefix any header name with `http_` to access its value. HTTP header names use hyphens; replace them with underscores in the variable name.

| Variable | Header |
|----------|--------|
| `http_user_agent` | `User-Agent` |
| `http_accept` | `Accept` |
| `http_content_type` | `Content-Type` |
| `http_authorization` | `Authorization` |
| `http_x_request_id` | `X-Request-Id` |

`http_*` variables always exist (they return an empty string if the header is absent). All other variables listed in the table above are known and validated at startup.

---

## Syntax

Two equivalent forms are supported:

```
$name          simple form — variable name ends at the first non-variable character
${name}        brace form — use when the variable is adjacent to other letters or digits
```

**Examples:**

```json
"X-Real-IP": "$remote_addr"
"X-Client":  "${remote_addr}:${remote_port}"
"X-Proto":   "forwarded-for=$remote_addr via $scheme"
```

A lone `$` with no valid variable name following it is treated as a literal `$`.

---

## Startup Validation

Variable names are validated when the configuration is compiled. Any unknown variable name (except the `http_*` family, which is always accepted) causes Switchyard to exit immediately with an error. This catches typos before any traffic is served.

---

## Where Variables Can and Cannot Be Used

Variables are available in:

- `set_headers` value templates
- `set_response_headers` value templates
- the `{var.NAME}` placeholder in log format strings
- `response` location bodies and headers
- the `backend_error`, `not_found`, and `overflow` response bodies and headers

They cannot be used in `path`, `root`, `listen`, or other configuration fields.
