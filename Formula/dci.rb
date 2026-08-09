class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.6.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.6.0/dci_1.6.0_darwin_arm64.tar.gz"
      sha256 "8f777ba67f2c1a5e75444042637f6288d08811fffb060026d7a5ec7f744aadf8"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.6.0/dci_1.6.0_darwin_amd64.tar.gz"
      sha256 "606382e5c06adf14eb9311ba25f7fda3fd9704be29738fd1ef3e8faa8d5b1d2a"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.6.0/dci_1.6.0_linux_arm64.tar.gz"
      sha256 "5ca1572a0827b4ff89986fd6b9ed66ce9d17431a28e2210347bad2a0fafb195c"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.6.0/dci_1.6.0_linux_amd64.tar.gz"
      sha256 "09747ef2b0bcc77cc6d6906afe923e838fee2e66f137f5c0d812cf55adf72d7d"
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
