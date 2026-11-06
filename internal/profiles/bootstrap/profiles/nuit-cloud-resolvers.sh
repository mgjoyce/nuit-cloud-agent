# Makes /etc/resolv.conf list the platform's resolvers (NCS_DNS_SERVERS, from the platform's own setting, never a request). Its own profile,
# applied on purpose from the portal or by the platform before something that needs the network. Changes one file and one companion file.
PROFILE_FEATURES="ncs_resolvers"
