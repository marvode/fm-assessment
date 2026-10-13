namespace Wallet.Common;

/// <summary>Success with a value, or an <see cref="Error"/>. Expected failures are returned, not thrown.</summary>
public sealed class Result<T>
{
    private readonly T? _value;
    private readonly Error? _error;

    private Result(T? value, Error? error)
    {
        _value = value;
        _error = error;
    }

    public bool IsSuccess => _error is null;

    public bool IsFailure => !IsSuccess;

    public T Value => IsSuccess
        ? _value!
        : throw new InvalidOperationException($"Result is a failure ({_error!.Code}); check IsSuccess first.");

    public Error Error => _error
        ?? throw new InvalidOperationException("Result is a success; it has no error.");

    public static Result<T> Success(T value) => new(value, null);

    public static Result<T> Failure(Error error) => new(default, error);

    public static implicit operator Result<T>(T value) => Success(value);

    public static implicit operator Result<T>(Error error) => Failure(error);
}
