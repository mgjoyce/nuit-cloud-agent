# Turns off SSH password logins and root login on the machine, safely: only if the login account already has a key the platform
# knows about, with a timer that undoes it unless the platform has tested sshd from outside and confirmed. Its own profile, applied
# on purpose from the portal. See `ncs_ssh_harden` in features.sh.
PROFILE_FEATURES="ncs_ssh_harden"
