class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "0.1.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v0.1.0/dci_0.1.0_darwin_arm64.tar.gz"
      sha256 "961d1700a447406380f471e2729cf7c0cb816a09cf43c146d30b81822a9f3d14"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v0.1.0/dci_0.1.0_darwin_amd64.tar.gz"
      sha256 "5b45023dbb824be5d5c1b7eb37c4c0cc16bdc66adbdf82e9f30f0cf4c0a3d141"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v0.1.0/dci_0.1.0_linux_arm64.tar.gz"
      sha256 "df89d819fbe0e16194847600df125fce53d194ee9edeee9c1e3d0450aab41668"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v0.1.0/dci_0.1.0_linux_amd64.tar.gz"
      sha256 "fe924ce44ae17309281f8ef1cc69dcfd3005ff6d515399d514f55fb72cfe4f51"
    end
  end

  def install
    bin.install "dci"
  end

  test do
    output = shell_output("#{bin}/dci --help")
    assert_match "DoiT Cloud Intelligence", output
  end
end
