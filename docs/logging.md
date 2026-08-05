# Logging

Switchyard supports custom log formatting via a `logging` configuration block. When logging is not configured, a built-in operational log line is written for each request instead.

Logging can be configured at two levels:
- **Global** (`logging` at the top level) — fires for every request
- **Location** (`logging` inside a location block) — fires only for requests matched by that location

Both fire independently when both are configured. Neither overrides the other; they each produce a separate log line from the same request record. See [concepts.md#stacking](concepts.md#stacking).

---

## Configuration

```json
{
    "logging": {
        "outputs": ["console", "file"],
        "file": "access.log",
        "format": "{receive_time} {method} {path} {status} {request_duration}"
    }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `outputs` | array of string | `["console"]` | Where to write log lines: `"console"` and/or `"file"` |
| `file` | string | — | Log file path; required when `"file"` is in `outputs` |
| `format` | string | required | Log line template; see [Format Fields](#format-fields) below |

`outputs` defaults to `["console"]` if omitted. Both `"console"` and `"file"` can appear together.

Log files are opened in append mode with permissions `0644`. The format is compiled once at startup; unknown field names cause an immediate exit.

---

## Format Fields

The `format` string uses `{field}` placeholders. Parameterized fields use `{group.PARAM}` syntax. Absent or zero values render as `-`.

### Request

| Placeholder | Description |
|-------------|-------------|
| `{method}` | HTTP method |
| `{url}` | Full request URL including scheme and host |
| `{path}` | Request path (no query string) |
| `{request_body}` | Full request body (buffered; see [Body Capture](#body-capture)) |

### Response

| Placeholder | Description |
|-------------|-------------|
| `{status}` | HTTP status code returned to the client |
| `{app_status}` | HTTP status code returned by the backend |
| `{response_body}` | Full response body (buffered; see [Body Capture](#body-capture)) |

### Backend

| Placeholder | Description |
|-------------|-------------|
| `{backend_id}` | ID of the backend that handled the request (or `-` if none) |

### Timing

All timestamps are in RFC3339 format with millisecond precision. Durations are in milliseconds.

| Placeholder | Description |
|-------------|-------------|
| `{receive_time}` | When Switchyard received the request from the client |
| `{forward_time}` | When Switchyard sent the request to the backend |
| `{app_response_time}` | When Switchyard received the response from the backend |
| `{end_time}` | When Switchyard finished sending the response to the client |
| `{request_duration}` | Total time from `receive_time` to `end_time` (ms) |
| `{app_duration}` | Backend round-trip time from `forward_time` to `app_response_time` (ms) |

### Parameterized Fields

These fields take a parameter after a dot:

| Placeholder | Description |
|-------------|-------------|
| `{req_header.NAME}` | Value of the `NAME` request header |
| `{resp_header.NAME}` | Value of the `NAME` response header |
| `{query.NAME}` | Value of the `NAME` query parameter |
| `{var.NAME}` | Value of the request [variable](variables.md) `NAME` |

**Examples:**

```
{req_header.User-Agent}       → User-Agent request header
{resp_header.Content-Type}    → Content-Type response header
{query.page}                  → ?page=... query parameter
{var.remote_addr}             → client IP address
```

---

## Format Examples

Minimal access log:
```
"{method} {path} {status}"
```

Nginx-style combined log:
```
"{var.remote_addr} {method} {request_uri} {status} {req_header.User-Agent}"
```

Timing-focused log:
```
"{receive_time} {method} {path} backend={backend_id} status={status} total={request_duration} app={app_duration}"
```

Debugging log with body:
```
"{method} {path} {status} req={request_body} resp={response_body}"
```

---

## Body Capture

Request and response bodies are only buffered when the format actually references them:

- `{request_body}` — causes Switchyard to read the full request body into memory before forwarding it. The body is restored so the backend still receives it.
- `{response_body}` — causes Switchyard to tee the full response body into memory as it is streamed to the client.

When neither field is referenced, bodies are not buffered and stream through without memory overhead.

**Warning:** Body buffering reads the entire body into memory. Avoid referencing `{request_body}` or `{response_body}` in high-traffic or large-payload environments unless necessary.

---

## Implementation Details

**`loggingTransport`** — When any logging is configured (globally or on any location), each backend's HTTP transport is wrapped in a `loggingTransport`. This wrapper records `forward_time`, `app_response_time`, and `app_status` by reading a `logRecord` off the request context. It is a no-op when no `logRecord` is present, so non-logged requests incur no additional overhead.

**`statusWriter`** — Wraps the `http.ResponseWriter` to capture the status code sent to the client. Also tees the response body when `{response_body}` is referenced. Correctly forwards `Flush` and `Hijack` calls, so streaming responses and WebSocket upgrades work normally.

**Serialization** — Each logger serializes its writes with a mutex to prevent interleaved log lines under concurrent requests.
