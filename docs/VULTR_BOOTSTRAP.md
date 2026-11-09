# Vultr bootstrap asset

The repository keeps the Debian bootstrap as a reviewed Bash template at
`scripts/bootstrap/minecraft-debian.sh.tmpl`. The checked-in asset contains no
deployment IP, Wasabi bucket or prefix, or provider credential. The Go
`gsc-bootstrap` command validates the deployment settings, renders the script
in memory, and creates a new Vultr startup script. It does not update or delete
existing startup scripts and does not create a VM.

The command requires a public IPv4 CIDR no broader than `/16`, two distinct
valid ports, and a safe Wasabi bucket or prefix. It defaults to a dry run. A dry run prints the startup-script name,
SHA-256 digest, and rendered size. It does not read the Vultr credential or
call the provider. Review the checked-in template and the deployment settings
before running the explicit upload command:

```text
gsc-bootstrap upload \
  --name gsc-minecraft-debian \
  --interserver-cidr <public-ipv4-cidr> \
  --minecraft-port <port> \
  --voice-chat-port <port> \
  --wasabi-remote-path <bucket-or-bucket-prefix>
```

After review, add `--execute --confirm-upload gsc-minecraft-debian` to upload
one new immutable version. The exact name confirmation is required. Supply the
Vultr key through the `systemd-credential://vultr-bootstrap-api-token`
credential reference in `CREDENTIALS_DIRECTORY`; never pass the key as an
argument or environment variable. If the create result is unknown, inspect the
Vultr account for the named script before retrying, because the API create is
not automatically retried.

The upload command prints the provider script ID and SHA-256 digest, but not
the rendered script or Wasabi path. Record those values with the deployment
configuration. This upload step does not attach the script to a VM. The
capacity-backed provision command must be extended to verify the recorded
script ID and digest and send `script_id` before new VMs can run this bootstrap.

The template carries forward the operator-provided bootstrap behavior. A
successful upload does not prove that a host booted successfully or that the
game, restore, firewall, and backup services work together. Validate the
rendered asset in an isolated disposable environment before attaching it to a
real provision flow.
