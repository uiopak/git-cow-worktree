using System;
using System.IO;
using System.Runtime.InteropServices;

// Edit only a shortcut's taskbar identity. Its launch target, arguments and
// icon remain under the existing shortcut's control.
public static class CowShortcutIdentity
{
    [StructLayout(LayoutKind.Sequential)]
    private struct PropertyKey
    {
        public Guid Format;
        public uint Id;
    }

    // PROPVARIANT's string pointer begins at offset 8 on both Windows ABIs.
    // Reserving 24 bytes also covers the largest union on 64-bit Windows.
    [StructLayout(LayoutKind.Explicit, Size = 24)]
    private struct PropVariant
    {
        [FieldOffset(0)] public ushort Type;
        [FieldOffset(8)] public IntPtr Value;
    }

    [ComImport, Guid("886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface PropertyStore
    {
        [PreserveSig] int GetCount(out uint count);
        [PreserveSig] int GetAt(uint index, out PropertyKey key);
        [PreserveSig] int GetValue(ref PropertyKey key, out PropVariant value);
        [PreserveSig] int SetValue(ref PropertyKey key, ref PropVariant value);
        [PreserveSig] int Commit();
    }

    [DllImport("shell32.dll", CharSet = CharSet.Unicode)]
    private static extern int SHGetPropertyStoreFromParsingName(string path, IntPtr context, uint flags, ref Guid iid, out PropertyStore store);
    [DllImport("shell32.dll", CharSet = CharSet.Unicode)]
    private static extern void SHChangeNotify(int change, uint flags, string item, IntPtr secondItem);
    [DllImport("ole32.dll")]
    private static extern int PropVariantClear(ref PropVariant value);

    private static PropertyKey AppIdKey()
    {
        return new PropertyKey { Format = new Guid("9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3"), Id = 5 };
    }

    private static PropertyStore Open(string shortcut, bool writable)
    {
        if (!File.Exists(shortcut) || !String.Equals(Path.GetExtension(shortcut), ".lnk", StringComparison.OrdinalIgnoreCase))
            throw new ArgumentException("Expected an existing .lnk shortcut: " + shortcut);
        Guid iid = typeof(PropertyStore).GUID;
        PropertyStore store;
        Marshal.ThrowExceptionForHR(SHGetPropertyStoreFromParsingName(Path.GetFullPath(shortcut), IntPtr.Zero, writable ? 2u : 0u, ref iid, out store));
        return store;
    }

    public static string Read(string shortcut)
    {
        PropertyStore store = Open(shortcut, false);
        try
        {
            PropertyKey key = AppIdKey();
            PropVariant value;
            Marshal.ThrowExceptionForHR(store.GetValue(ref key, out value));
            try { return value.Type == 31 ? Marshal.PtrToStringUni(value.Value) : null; }
            finally { PropVariantClear(ref value); }
        }
        finally { Marshal.ReleaseComObject(store); }
    }

    public static void Set(string shortcut, string appId)
    {
        if (String.IsNullOrEmpty(appId) || appId.Length > 128)
            throw new ArgumentException("App ID must contain 1 to 128 characters.");
        foreach (char character in appId)
            if (Char.IsWhiteSpace(character) || character == '\0')
                throw new ArgumentException("App ID must not contain whitespace or NUL.");

        PropertyStore store = Open(shortcut, true);
        try
        {
            PropertyKey key = AppIdKey();
            PropVariant value = new PropVariant { Type = 31, Value = Marshal.StringToCoTaskMemUni(appId) };
            try
            {
                Marshal.ThrowExceptionForHR(store.SetValue(ref key, ref value));
                Marshal.ThrowExceptionForHR(store.Commit());
            }
            finally { PropVariantClear(ref value); }
        }
        finally { Marshal.ReleaseComObject(store); }
        SHChangeNotify(0x2000, 0x0005, Path.GetFullPath(shortcut), IntPtr.Zero);
    }
}
