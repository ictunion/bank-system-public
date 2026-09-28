{ buildNpmPackage
, lib
, src
  # VITE_* vars are baked into the bundle at build time — a runtime .env has no effect.
  # No defaults: callers must pass them explicitly.
, keycloakUrl
, keycloakRealm
, keycloakClientId
}:
buildNpmPackage {
  pname = "bank-frontend";
  version = "0.1.0";
  inherit src;

  npmDepsHash = "sha256-EXg/HYXdmVg7D8+d1GCJI9PDgz9BoZYZS108NFPchAc=";

  env = {
    VITE_KEYCLOAK_URL = keycloakUrl;
    VITE_KEYCLOAK_REALM = keycloakRealm;
    VITE_KEYCLOAK_CLIENT_ID = keycloakClientId;
  };

  installPhase = ''
    mkdir -p $out/var/www
    cp -r dist/* $out/var/www
  '';
}
