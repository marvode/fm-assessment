using System.Text.Json.Serialization;
using Wallet.Ledger.Payouts;

namespace Wallet.Ledger.Outbox;

[JsonSerializable(typeof(PayoutRequested))]
internal sealed partial class WalletJsonContext : JsonSerializerContext
{
}
