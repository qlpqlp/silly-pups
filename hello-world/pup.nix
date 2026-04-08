# PupWizard generated pup.nix (darkhttpd stub) — Hello World Pup
# DogeBox ExecStart uses pkgs.pup.hello-world — this attr MUST match manifest container.services[0].name exactly.
{ pkgs ? import <nixpkgs> {} }:

let
  indexHtml = pkgs.writeText "index.html" ''
<!doctype html><meta charset=utf-8><title>DogeBox PUP</title><h1>Such PUP. Much DogeBox.</h1><p>PupWizard placeholder page — replace pup.nix with your real build.</p>
  '';
  wwwRoot = pkgs.runCommand "pupwizard-www" {} ''
    mkdir -p $out
    ln -sf ${indexHtml} $out/index.html
  '';
  pupRun = pkgs.writeShellScriptBin "run.sh" ''
    set -e
    PORT="''${PORT:-8080}"
    exec ${pkgs.darkhttpd}/bin/darkhttpd ${wwwRoot} --port "''${PORT}"
  '';
  svcName = "hello-world";
in
{
  ${svcName} = pupRun;
}
