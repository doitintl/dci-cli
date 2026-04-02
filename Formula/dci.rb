class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.2.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.2.0/dci_1.2.0_darwin_arm64.tar.gz"
      sha256 "bf55a4cd803a0a1e3bfc86126d46f87908e6c957836f016ffdb4740060c86d55"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.2.0/dci_1.2.0_darwin_amd64.tar.gz"
      sha256 "050c556f5979ce895c3842096cb629f691226a3c446f45c9e49f176db3a61a85"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.2.0/dci_1.2.0_linux_arm64.tar.gz"
      sha256 "551de4754544b6ffa06e900113aa93a635d74b531f03d502ba5b08c84553a52f"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.2.0/dci_1.2.0_linux_amd64.tar.gz"
      sha256 "21bb8a9b5dad10c87d44f655a06eae5106ae92fac83e59ead05705ba847b29b9"
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
