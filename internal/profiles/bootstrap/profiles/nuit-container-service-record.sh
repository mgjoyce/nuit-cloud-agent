# Records a Nuit Container Service runtime that is already installed and running (put there by hand before the profile existed): compares
# what is installed with the pinned bundle and, only if it is that bundle, writes the platform's record of it (the smoke script and the
# bundle's SHA-256). Installs nothing, restarts nothing, changes no configuration.
PROFILE_FEATURES="oci_saas_record"
