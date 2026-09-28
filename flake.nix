{
  description = "bank-system Go API dev shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            golangci-lint
            sqlc
            goose
            postgresql
            go-swag

            # frontend/ (React + Vite) — bundles npm
            nodejs_22
          ];

          shellHook = ''
            export GOPATH="$PWD/.gopath"
            export PATH="$GOPATH/bin:$PATH"
          '';
        };

        packages = rec {
          bank-api = pkgs.callPackage ./nix/bank-api.nix {
            src = self;
          };

          bank-frontend = pkgs.callPackage ./nix/bank-frontend.nix {
            src = "${self}/frontend";
            # Local dev values (see frontend/.env.example).
            # Deployments must override these with `bank-frontend.override { ... }`.
            keycloakUrl = "http://localhost:8180";
            keycloakRealm = "members";
            keycloakClientId = "bank-system";
          };

          default = bank-api;
        };

      });
}
