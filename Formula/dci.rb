class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.0.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.0.1/dci_1.0.1_darwin_arm64.tar.gz"
      sha256 "03aa68a5bbc42322d02dbe3a4c9383d511aa676be44022316dcfa1a24a198a60"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.0.1/dci_1.0.1_darwin_amd64.tar.gz"
      sha256 "bb6a8dc79ee13f804fb96eb729703d2db174ae31600b85a40549b398f671dc0b"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.0.1/dci_1.0.1_linux_arm64.tar.gz"
      sha256 "061e7e8bf961de6285fe1efeebedb6049816c100001d089cc88732027242a055"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.0.1/dci_1.0.1_linux_amd64.tar.gz"
      sha256 "b20fc3f5946625a4f6bd7803bf90f369d64e75502fc62caffe66d2fd98d6054d"
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
