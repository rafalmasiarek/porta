class Porta < Formula
  desc "Portable automation runtime with USB triggers, hooks, and offline-first execution"
  homepage "https://github.com/rafalmasiarek/porta"
  url "https://github.com/rafalmasiarek/porta/releases/download/v4.3.0/porta-darwin-amd64"
  sha256 "0fc595557e5853ec0163385c27ff9b84393aa29bb94496b5ec2275578db247e6"
  version "4.3.0"

  def install
    bin.install "porta-darwin-amd64" => "porta"
  end
end
