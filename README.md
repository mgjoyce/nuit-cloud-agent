# nuit-cloud-agent

The program a **Nuit Cloud Server** runs so the nuit·cloud platform can commission and look after it.

- It **only calls out** to the platform over HTTPS; it listens on nothing. No SSH, no `sudo` list, no root password held by the platform.
- It does **only what the platform signs**, and only tasks in its own closed catalog: `ping`, `survey`, `profile-check`, `profile-apply`. A task is run only if its Ed25519 signature verifies against a key the agent was given at enrollment, it is for this machine, it has not expired, and it has not been run before.
- One static binary (no runtime on the machine), for linux/amd64 and linux/arm64.

The design and the reasons are in the platform repository: `docs/adr/0001-machines-are-commissioned-by-a-pull-agent.md`.

## Using it

```
# once, as root, with an enrollment code made in the admin portal (Platform tenant):
nuit-cloud-agent enroll --url https://platform.example --code nce_... 

# what the service runs:
nuit-cloud-agent run

nuit-cloud-agent status     # who this machine is on the platform (never the credential)
nuit-cloud-agent survey     # read this machine and print what the platform would be told; changes nothing
```

`--url` must be `https://`. `--insecure-http` allows plain `http://` for a trusted test network only, because the enrollment code and the agent's credential must not cross a network in the clear.

The agent keeps its identity in `/var/lib/nuit-cloud-agent/state.json` (mode 0600, root only), written atomically.

## Development

```
make test      # go vet and go test
make build     # static binaries in dist/ (VERSION=1.2.3 to set the version)
```

`internal/taskverify/testdata/vectors.json` holds tasks signed by the platform's own TypeScript signing code with a public test key, so the agent is tested against exactly what the platform produces. Regenerate it from the platform repository if the signing format ever changes.

## Status

First version (0.1.0). Not yet built: self-update, the `profile-check` / `profile-apply` tasks (they answer "not supported by this version"), release signing, and automatic credential rotation. See the tickets in the platform repository under the epic "Commission a Nuit Cloud Server from the admin portal".
