# A Nuit Cloud Server that plays the Nuit Container Service role: the base profile plus hardware virtualisation (/dev/kvm),
# cgroup v2 and the runtime itself. Tenants' containers then run on it as microVMs.
. "$here/profiles/nuit-cloud-server.sh"
PROFILE_FEATURES="$PROFILE_FEATURES kvm cgroup2 oci_saas_runtime"
