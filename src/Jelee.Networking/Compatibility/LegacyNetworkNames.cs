namespace Jelee.Networking.Compatibility;

/// <summary>
/// Names retained at the legacy client discovery and configuration boundaries.
/// </summary>
internal static class LegacyNetworkNames
{
    /// <summary>
    /// Existing client discovery request, matched without regard to letter case.
    /// </summary>
    public const string DiscoveryRequest = "who is JellyfinServer?";

    /// <summary>
    /// Existing configuration option used by the preserved server entry point.
    /// </summary>
    public const string PublishedServerUrlEnvironment = "JELLYFIN_PublishedServerUrl";
}
