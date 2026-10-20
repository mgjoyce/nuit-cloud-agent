# Puts the machine on the tenant VLAN with the slice the platform allocated for it: an address of its own and the block of
# addresses for tenants, on a VLAN sub-interface of the interface the machine already uses. Its own profile, never part of a
# base, applied on purpose from the portal with the slice in the signed task. See `ncs_vlan` in features.sh for the safeguards.
# The VLAN is assumed to be carried to the machine's port (tagged) already: that is a decision about the network made elsewhere.
PROFILE_FEATURES="ncs_vlan"
