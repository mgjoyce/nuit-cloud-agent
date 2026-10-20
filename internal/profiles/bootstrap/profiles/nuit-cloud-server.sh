# The base of every Nuit Cloud Server (a machine under the platform's control): the platform's login, automatic security
# updates, a synchronised clock, a bounded journal, the packages the agent and runtime need, a wired connection and SSH that
# does not allow passwords. A profile is what makes a server play a role; this one plays none by itself.
PROFILE_FEATURES="user updates timesync journal ncs_packages ncs_wired ncs_ssh"
