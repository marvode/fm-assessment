using System.Text.Json.Serialization;
using Live.Wallet.Payouts;

namespace Live.Wallet.Outbox;

[JsonSerializable(typeof(PayoutRequested))]
internal sealed partial class WalletJsonContext : JsonSerializerContext
{
}
