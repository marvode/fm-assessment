using Microsoft.EntityFrameworkCore.Diagnostics;

namespace Live.Tests.Wallet;

/// <summary>Runs another writer's change just before this context saves, to force a concurrency conflict deterministically.</summary>
internal sealed class ConcurrentWriteInterceptor : SaveChangesInterceptor
{
    private readonly Func<Task> _concurrentWrite;
    private int _remaining;

    public ConcurrentWriteInterceptor(int times, Func<Task> concurrentWrite)
    {
        _remaining = times;
        _concurrentWrite = concurrentWrite;
    }

    public override async ValueTask<InterceptionResult<int>> SavingChangesAsync(
        DbContextEventData eventData,
        InterceptionResult<int> result,
        CancellationToken cancellationToken = default)
    {
        if (_remaining > 0)
        {
            _remaining--;
            await _concurrentWrite();
        }

        return result;
    }
}
