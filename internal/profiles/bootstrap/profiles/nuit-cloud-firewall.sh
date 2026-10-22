# A baseline inbound firewall: SSH from the platform's ranges and the ports the platform names, everything else dropped. Guarded: a timer
# removes it again unless the platform has tested from outside and confirmed. Its own profile, applied on purpose from the portal.
# See `ncs_firewall` in features.sh.
PROFILE_FEATURES="ncs_firewall"
