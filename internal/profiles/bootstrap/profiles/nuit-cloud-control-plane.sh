# What a control-plane host runs for the platform: the scheduler, the agent's https front and a dyndns probe, each a container started
# at boot by its own systemd unit (no platform needed), from images the signed task pins by digest. The secrets are not in the task: the
# agent fetches them, writes them to root-only files and tells these features only where they are. A service the task does not name is left alone.
PROFILE_FEATURES="docker docker_logs ncs_cp_scheduler ncs_cp_tls_front ncs_cp_probe"
