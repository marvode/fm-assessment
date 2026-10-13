namespace Practice.Common;

public interface IClock
{
    DateTimeOffset UtcNow { get; }
}
