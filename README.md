# outbox

outbox catches what your app sends through email and text-message vendors' APIs. It answers Resend, Amazon SES and Amazon SNS the way they do, passes the emails on to an SMTP inbox such as [Mailpit](https://mailpit.axllent.org), and keeps the text messages, so nothing your app sends leaves.

Your app keeps its code: the vendors' SDKs read their address from the environment, and outbox is at that address.

It's made for preview environments and local development: a preview of your app can sign people up, send them codes and receipts, and no real person gets a message. [Stagd](https://stagd.dev) runs it in previews for mail and text capture.

> **Status:** a first version, moved out of the Stagd CLI (`stagd mock`); no release or published image yet.

## What it answers

| Vendor | Point its SDK at outbox with | What outbox does |
|---|---|---|
| Resend | `RESEND_BASE_URL=http://outbox:8080/resend` (Node, Go, Ruby, PHP), `RESEND_API_URL` (Python) | Sends (one and batch) and gets: emails go on to SMTP. |
| Amazon SES | `AWS_ENDPOINT_URL_SES=http://outbox:4566`, `AWS_ENDPOINT_URL_SESV2` too | SendEmail and SendRawEmail (the first API) and SendEmail (v2, simple, raw or a template): emails go on to SMTP. |
| Amazon SNS | `AWS_ENDPOINT_URL_SNS=http://outbox:4566` | Publish to a phone number: the text message is kept. A publish to a topic is refused. |

Amazon's APIs have a port of their own, 4566: the AWS SDKs take an endpoint as a host, and SES's first API and SNS both answer at `/`. The AWS SDKs sign their requests; outbox doesn't check the signatures.

The text messages are kept in memory, the last 500, and read with `GET /texts` on port 8080:

```json
{"texts": [{"id": "…", "vendor": "sns", "to": "+4791234567", "body": "Your code is 123456", "time": "2026-11-02T14:00:00Z"}]}
```

Newest first. An email outbox passes on has `X-Outbox-Vendor` saying which vendor's API the app called.

## Running it

Beside your app and Mailpit:

```yaml
services:
  mailpit:
    image: axllent/mailpit
  outbox:
    image: ghcr.io/stagdhq/outbox
    environment:
      OUTBOX_SMTP: mailpit:1025
  app:
    build: .
    environment:
      RESEND_BASE_URL: http://outbox:8080/resend
      AWS_ENDPOINT_URL_SES: http://outbox:4566
      AWS_ENDPOINT_URL_SNS: http://outbox:4566
```

Until the image is published, build it with `docker build -t outbox .` and use `image: outbox`. On your machine without Docker: `outbox serve --smtp localhost:1025`.

`outbox serve` takes each setting as a flag or from its variable:

| Flag | Variable | Default | |
|---|---|---|---|
| `--listen` | `OUTBOX_LISTEN` | `:8080` | Where the vendors with JSON APIs (Resend) are answered, under `/<vendor>`, and `/texts`. |
| `--aws-listen` | `OUTBOX_AWS_LISTEN` | `:4566` | Where Amazon SES and SNS are answered. |
| `--smtp` | `OUTBOX_SMTP` | | The SMTP server emails go on to, `host:port` (Mailpit's is `:1025`). Empty logs them only. |
| `--log-format` | `OUTBOX_LOG_FORMAT` | `text` | `text` or `json`. |
| `--log-level` | `OUTBOX_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |

### Health

`outbox health` exits 0 once both ports answer, for a container's health check; the image uses it as its own. Over HTTP it's `GET /healthz` on either port.

```yaml
    healthcheck:
      test: ["CMD", "/outbox", "health"]
```

### OpenTelemetry

outbox sends traces and metrics over OTLP/HTTP when an endpoint is set, with the standard variables: `OTEL_EXPORTER_OTLP_ENDPOINT` (or `_TRACES_ENDPOINT` and `_METRICS_ENDPOINT`), `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_SERVICE_NAME` (`outbox` if not set), `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_METRIC_EXPORT_INTERVAL`. Without one it sends nothing.

- **A span for each request,** joining the app's trace if it sent one (`traceparent`), named for what the app asked: its route (`POST /resend/emails`) or, at Amazon's root, its action (`AWS SendEmail`, `AWS Publish`, `AmazonSNS.Publish`). Health checks are left out.
- **Metrics,** each by `outbox.vendor` (`resend`, `ses`, `sns`):

| Metric | | |
|---|---|---|
| `outbox.emails` | counter | Emails caught, also by `outbox.outcome`: `passed` on to the SMTP inbox, `kept` (no SMTP set), or `failed` (the inbox refused or couldn't be reached). |
| `outbox.smtp.duration` | histogram, s | How long passing an email on to the SMTP inbox took. |
| `outbox.texts` | counter | Text messages caught. |

  OpenTelemetry's own HTTP metrics come too (`http.server.request.duration`).

`outbox version` prints the version.

## Later

- **More vendors:** Twilio for text messages, SendGrid, Postmark and Mailgun for email, where their SDKs read their address from the environment.
- **Delivery webhooks back to the app:** delivered, bounced, complained, as the vendors call them.
- **Inbound messages:** a reply text or email posted to the app's webhook, for two-way flows.
- **Waiting for a message,** for end-to-end tests: the next text to a number, and its code.
- **Text messages on a volume,** so they outlast a restart.

## Developing

```bash
go test ./...
```

[Task](https://taskfile.dev) is pinned in `go.mod`: `go tool task check` runs what CI does, `go tool task image` builds the image, and `go tool task --list` shows the rest. A `v*` tag releases: the binaries and checksums on its GitHub release, and the image on ghcr.io.

## License

outbox is licensed under the [Apache License 2.0](LICENSE).
