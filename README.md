# tunnelssh
reverse tunnels ssh over websocket

## Master metrics

`tunnelssh master --metrics-addr :9182` serves Prometheus metrics at
`/metrics` on that address, apart from `--port` so that whatever exposes
the master's port does not expose them too. Without the flag none are served.

- `tunnelssh_agent_sessions`: agents connected now.
- `tunnelssh_agent_connected{id}`: 1 for each connected agent id.
- `tunnelssh_agent_connects_total`, `tunnelssh_agent_disconnects_total`:
  sessions registered and ended, to catch an agent that flaps.
- `tunnelssh_agent_rejected_total{reason}`: refused connections;
  `duplicate_id` (the id already has a session) or `handshake` (the
  websocket upgrade or the yamux session failed).
