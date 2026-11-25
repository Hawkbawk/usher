{
  description = "usher - real HTTPS hostnames for local machines";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs, ... }:
    let
      # Darwin-only for now: the daemon binds macOS-specific loopback alias and
      # DNS resolver mechanisms (see internal/netalias), so there's no Linux
      # support to build or cache yet.
      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      mkUsher =
        { lib, buildGoModule }:
        buildGoModule (finalAttrs: {
          pname = "usher";
          version = "0.1.0";

          src = lib.cleanSource ./.;

          vendorHash = "sha256-A6Bo0zFcG6bIYvaA8f2reP/QcIbzFSgWIFkHeWJXsa4=";

          ldflags = [
            "-s"
            "-w"
            "-X=github.com/hawkbawk/usher/cmd.version=${finalAttrs.version}"
          ];

          # The Caddyfile adapter test is what proves the deSEC plugin is still wired
          # up, so it is worth paying for on every build.
          doCheck = true;

          # Deliberately not wrapped with a PATH: usher shells out to `sbx` and
          # `orbctl`, which come from Homebrew or OrbStack on this machine.
          # Pinning them to nixpkgs copies would find the wrong binaries.

          meta = {
            description = "Real HTTPS hostnames for sandboxes, VMs, containers, and bare ports";
            longDescription = ''
              usher shows every request to the right door. It gives anything running
              locally a real hostname
              behind a publicly trusted wildcard certificate: a Docker Sandbox, an
              OrbStack VM, a container, or a bare ip:port. It embeds Caddy with the
              deSEC DNS-01 provider and a minimal authoritative DNS server, so there
              is no separate Caddy build and no dnsmasq to install.
            '';
            homepage = "https://github.com/hawkbawk/usher";
            license = lib.licenses.mit;
            mainProgram = "usher";
            platforms = lib.platforms.darwin;
          };
        });
    in
    {
      packages = forAllSystems (pkgs: rec {
        usher = pkgs.callPackage mkUsher { };
        default = usher;
      });

      overlays.default = final: _prev: {
        usher = final.callPackage mkUsher { };
      };

      # The CLI half. Installs the binary for a user.
      homeModules.usher = import ./nix/home-module.nix self;

      # The daemon half. Needs root to bind 443, add the loopback alias, and
      # write /etc/resolver, so it cannot live in home-manager.
      darwinModules.usher = import ./nix/darwin-module.nix self;

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gopls
            pkgs.go-tools
            pkgs.nixfmt
            pkgs.nix-update

            # For `go generate ./...`, which regenerates the daemon API stubs
            # from proto/usher/v1/usher.proto. The generated .pb.go files are
            # checked in, so a plain `nix build` never needs these.
            pkgs.protobuf
            pkgs.protoc-gen-go
            pkgs.protoc-gen-go-grpc
          ];
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
