# Contributing

`direxio-connect` is maintained for Direxio deployments only.

Before opening a change, check that it preserves these boundaries:

- no non-Matrix chat platform integrations;
- no public Matrix account or portal-owner session in examples;
- no legacy `!agent:<domain>` pseudo room ids;
- npm package remains `direxio-connent`;
- binary and operator docs use `direxio-connect`;
- repository links point to `https://github.com/YingSuiAI/direxio-connect`.

Run focused tests for the touched packages and `make build` before submitting changes.
