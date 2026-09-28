{ buildGoModule, lib, src }:
buildGoModule {
  pname = "bank-api";
  version = "0.1.0";
  inherit src;
  subPackages = [ "cmd/server" ];

  # Dependencies are vendored in ./vendor
  vendorHash = null;

  doCheck = false;
}
