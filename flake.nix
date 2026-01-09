{
  description = "tk - minimal ticket system";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
  };

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
    in
    {
      packages.${system}.default = pkgs.buildGoModule {
        pname = "tk";
        version = "0.1.0";
        src = ./.;
        vendorHash = "sha256-sZIpgET1mbDivyeR5XHVYjyUEq2X376vVZ3RZuVokQ8=";
      };
    };
}
