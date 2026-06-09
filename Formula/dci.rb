class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.4.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.0/dci_1.4.0_darwin_arm64.tar.gz"
      sha256 "eed65aba57dbe5d091b0181d02f558a7722c1d03c0d97001408936ebfc567251"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.0/dci_1.4.0_darwin_amd64.tar.gz"
      sha256 "1a439dae5acd4ff09723cb10996ce95311a4ee0af8a7ccc79094f7ba2d7b1e07"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.0/dci_1.4.0_linux_arm64.tar.gz"
      sha256 "5c39044010b6a7dca899f0386ed6a07c54a2b7e461cdcfe8989ba5f8cdb01709"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.4.0/dci_1.4.0_linux_amd64.tar.gz"
      sha256 "4ed267e813c7f85c40df64441a6e4fc8a510422f263558c24888c65714e9acba"
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
