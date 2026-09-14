{
  description = "snapwatch — watch a directory, snapshot every change, browse diffs live";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" "x86_64-darwin" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in {
      packages = forAll (pkgs: rec {
        snapwatch = pkgs.buildGoModule {
          pname = "snapwatch";
          version = "0.1.0";
          ldflags = [ "-s" "-w" "-X main.version=0.1.0" ];
          src = ./.;
          vendorHash = "sha256-UyAg6KWRmQwFi18IfmsEfjoByQ0y9B9eYbktae7nxsE=";
          nativeBuildInputs = [ pkgs.makeWrapper ];
          nativeCheckInputs = [ pkgs.git ];
          postInstall = ''
            wrapProgram $out/bin/snapwatch --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.git ]}
          '';
        };
        default = snapwatch;
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [ go gopls git watchexec ];
        };
      });
    };
}
