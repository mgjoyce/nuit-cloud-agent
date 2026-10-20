# Makes the machine use the management address the platform allocated for it, as a static address, instead of whatever DHCP
# gives. Its own profile, never part of the base: it changes the machine's network, so it is only ever applied on purpose, from
# the portal, with the address in the signed task. See `ncs_address` in features.sh for the safeguards.
PROFILE_FEATURES="ncs_address"
