class Porta < Formula
  desc "Portable automation runtime with USB triggers, hooks, and offline-first execution"
  homepage "https://github.com/rafalmasiarek/porta"
  url "https://github.com/rafalmasiarek/porta/releases/download/v4.4.1/porta-darwin-amd64"
  sha256 "ae461dac92b4c237e0134887afdf47d0f97edae36d9b572eb2157fdfdff2f224"
  version "4.4.1"

  def install
    bin.install "porta-darwin-amd64" => "porta"
  end
end
