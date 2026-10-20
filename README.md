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

## Installing on a machine by hand

Until the one-command installer exists (it needs an https address for the platform and published, signed releases). On the machine, as root (`su -`):

```
# 1. the binary for the machine's CPU (uname -m: x86_64 = amd64, aarch64 = arm64), copied over, for example with scp
install -m 0755 nuit-cloud-agent-linux-amd64 /usr/local/bin/nuit-cloud-agent

# 2. the service
install -m 0644 nuit-cloud-agent.service /etc/systemd/system/nuit-cloud-agent.service

# 3. enrol with a code from the admin portal (Platform tenant > Nuit Cloud Servers > Enrollment codes).
#    The portal shows this exact command. --insecure-http is needed only while the platform has no https address.
nuit-cloud-agent enroll --url http://<platform address>:4173 --code nce_... --insecure-http

# 4. start it. On a plain-http test network the service needs the same allowance:
mkdir -p /etc/systemd/system/nuit-cloud-agent.service.d
printf '[Service]\nEnvironment=NUIT_CLOUD_AGENT_INSECURE_HTTP=1\n' > /etc/systemd/system/nuit-cloud-agent.service.d/insecure-http.conf
systemctl daemon-reload && systemctl enable --now nuit-cloud-agent
journalctl -u nuit-cloud-agent -f      # it should say it is calling the platform
```

The machine appears in the portal as soon as it enrols. Then run **Survey now** on its page.

## Development

```
make test      # go vet and go test
make build     # static binaries in dist/ (VERSION=1.2.3 to set the version)
```

`internal/taskverify/testdata/vectors.json` holds tasks signed by the platform's own TypeScript signing code with a public test key, so the agent is tested against exactly what the platform produces. Regenerate it from the platform repository if the signing format ever changes.

## Status

Version 0.2.x. Built: enrolment, check-in, signed-task verification, `ping`, `survey`, `profile-check`, `profile-apply`. Not yet built: self-update, signed releases, and automatic credential rotation. See the tickets in the platform repository under the epic "Commission a Nuit Cloud Server from the admin portal".

## License

MIT. See LICENSE.
