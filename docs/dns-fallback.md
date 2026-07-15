# DNS Fallback

Optional. Start the server with the authoritative domain agents will use:

```sh
./sable-server --password-file ./pw.txt --dns-domain c2.example.com
```

`SABLE_DNS_DOMAIN=c2.example.com` or `DNS_DOMAIN=c2.example.com` work too. The UDP `:53` listener comes up and accepts beacon queries under that domain.

Build agents with the same domain:

```sh
make build-agent-linux DNS_DOMAIN=c2.example.com
```

The agent tries HTTPS first and falls back to DNS if HTTPS is unreachable. UDP 53 has to be reachable and the NS record needs to point to the Sable server. A fresh nonce is used for the fallback attempt so a lost HTTPS response cannot make the DNS retry look like a replay.

Beacon requests are capped at 15 KiB and task responses are split into retrievable 1,200-byte frames, keeping each UDP response under the advertised 4,096-byte EDNS size. The server retains response frames briefly so the agent can recover a lost final response without re-executing a task. Large uploads and results should stay on HTTPS; if a result cannot fit the DNS request limit, the agent returns an explicit transport error instead of permanently blocking its queue.
