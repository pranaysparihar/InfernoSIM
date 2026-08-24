# MCP stdio reliability fixture

This synthetic transcript demonstrates protocol-clean MCP stdio replay, an
ambiguous committed side effect, selected-fault coverage, and assertion reports.

```bash
printf '%s\n' '{"jsonrpc":"2.0","id":"runtime-1","method":"tools/call","params":{"name":"payment.refund","arguments":{"payment_id":"pay_1"}}}' | \
  infernosim agent mcp replay examples/mcp-stdio-reliability \
    --fault refund-response-lost \
    --proof /tmp/infernosim-mcp-proof.json \
    --report-dir /tmp/infernosim-mcp-report
```

The response is deliberately withheld, so stdout stays empty. The process exits
successfully because the effect committed exactly once and the selected fault
was reached. The fixture contains no production data or credentials.
