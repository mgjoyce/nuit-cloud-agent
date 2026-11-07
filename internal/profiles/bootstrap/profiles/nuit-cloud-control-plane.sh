# What a control-plane host runs for the platform: the scheduler (Ofelia and the sweep scripts), the agent's https front (Debian's nginx) and a dyndns probe,
# each a native systemd service that starts at boot with no platform and no container engine. The signed task names the services and the bundle they run
# from; the agent unpacks the bundle and writes the secrets to root-only files, and tells these features only where they are. A service the task does not
# name is left alone.
PROFILE_FEATURES="ncs_cp_scheduler ncs_cp_tls_front ncs_cp_probe"
