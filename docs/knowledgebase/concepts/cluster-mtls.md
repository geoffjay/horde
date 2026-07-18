---
type: Concept
title: Cluster mTLS
description: Mutual TLS as the intended long-term node→node authentication, and why a shared bearer token is the interim.
tags: [cluster, security, auth, mtls, future]
timestamp: 2026-07-17T00:00:00Z
---

Node→node cluster calls (register, heartbeat, event fan-out, and forwarded
spawn/invoke) are authenticated today by a **shared bearer token**
(`cluster.auth_token`; see [Environment](environment.md)) and gossip traffic is
encrypted with a shared symmetric key (`cluster.gossip_encryption_key`). This is
the pragmatic interim. The intended long-term posture is **mutual TLS**. This
doc records what that entails so it can be planned; it is *not* implemented.

# Why mTLS over a shared token

A single shared token is coarse: every node holds the same secret, it does not
identify *which* node is calling, it cannot be revoked per-node, and it rides on
plaintext HTTP (only the gossip channel is encrypted). mTLS fixes all three:
each node presents its own certificate, so the receiver authenticates the peer's
identity, encrypts the transport, and can revoke one node without re-keying the
cluster.

# What mTLS would require

1. **A cluster CA + per-node certificates.** A certificate authority for the
   cluster (self-managed, or an external PKI / SPIFFE) issues each node a
   key/cert with its node id as the subject/SAN. Nodes trust the CA.
2. **TLS listeners for node→node traffic.** The node HTTP server terminates TLS
   and requires+verifies client certs on the cluster endpoints
   (`tls.Config{ClientAuth: RequireAndVerifyClientCert, ClientCAs: caPool}`). The
   outbound `leaderClient` / spawn / invoke-proxy clients present the node's cert
   (`tls.Config{Certificates, RootCAs}`). This replaces the plain-`http://`
   transport used across nodes today (`leaderclient.go`, `placement.go`,
   `invoke.go`'s reverse proxy) — the discoverer-resolved address would be dialed
   as `https://`.
3. **Config + distribution.** New config for cert/key/CA paths (files, not
   inline). Certificates must be provisioned to each node and rotated before
   expiry — the operational cost mTLS adds over a static token.
4. **Client vs cluster separation.** mTLS secures node→node. The human-facing
   API (`/agents`, `/invoke`, the TUI's cluster reads) is a separate axis; it
   would keep its own auth story (a logged follow-up) rather than requiring
   client certs from every TUI.
5. **Gossip.** memberlist has its own transport; its `SecretKey` symmetric
   encryption stays the mechanism there (mTLS covers the HTTP API, not the
   gossip UDP/TCP). The two are complementary.

# Migration shape (when planned)

Keep the shared token working, add mTLS alongside it, and let a node prefer mTLS
when certs are configured (token as fallback), so a cluster can roll over node by
node. The `Discoverer` abstraction and the existing outbound call sites are the
seams: switching their scheme to `https` and attaching a client cert is the bulk
of the change; no change to discovery, register, or routing logic. See the
[master/slave model](../decisions/master-slave-model.md) decision for the
topology this secures.
