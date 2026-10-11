// Read-only Windows extent queries. See FSCTL_GET_RETRIEVAL_POINTERS in the SDK.
using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using Microsoft.Win32.SafeHandles;

namespace CowSmoke
{
    public sealed class Extent
    {
        public long Start;
        public long End;
        public long Lcn;
    }

    public sealed class Layout
    {
        public ulong Volume;
        public string FileId;
        public long ClusterBytes;
        public long Length;
        public Extent[] Extents;
    }

    public static class ExtentProbe
    {
        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool DeviceIoControl(SafeFileHandle handle, uint code,
            byte[] input, uint inputSize, byte[] output, uint outputSize,
            out uint returned, IntPtr overlapped);

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool GetFileInformationByHandleEx(SafeFileHandle handle,
            int infoClass, byte[] info, uint size);

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool GetDiskFreeSpace(string root, out uint sectors,
            out uint sectorBytes, out uint freeClusters, out uint totalClusters);

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern uint GetFinalPathNameByHandle(SafeFileHandle handle,
            StringBuilder path, uint capacity, uint flags);

        private static byte[] FileIdentity(SafeFileHandle handle)
        {
            // FileIdInfo uses ReFS's full 128-bit ID, unlike legacy file IDs.
            var identity = new byte[24];
            if (!GetFileInformationByHandleEx(handle, 18, identity, 24))
                throw new Win32Exception(Marshal.GetLastWin32Error());
            return identity;
        }

        private static string VolumeRoot(SafeFileHandle handle)
        {
            var path = new StringBuilder(512);
            uint length = GetFinalPathNameByHandle(handle, path, (uint)path.Capacity, 1); // VOLUME_NAME_GUID
            if (length >= path.Capacity)
            {
                path = new StringBuilder(checked((int)length + 1));
                length = GetFinalPathNameByHandle(handle, path, (uint)path.Capacity, 1);
            }
            if (length == 0) throw new Win32Exception(Marshal.GetLastWin32Error());
            if (length >= path.Capacity) throw new IOException("Final file path is too long.");
            string final = path.ToString();
            int end = final.IndexOf('\\', 4);
            if (!final.StartsWith(@"\\?\Volume{", StringComparison.OrdinalIgnoreCase) || end < 0)
                throw new IOException("File does not have a local volume GUID path.");
            return final.Substring(0, end + 1);
        }

        public static bool SameFile(string first, string second)
        {
            using (var a = new FileStream(first, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete))
            using (var b = new FileStream(second, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete))
            {
                var x = FileIdentity(a.SafeFileHandle);
                var y = FileIdentity(b.SafeFileHandle);
                for (int i = 0; i < x.Length; i++) if (x[i] != y[i]) return false;
                return true;
            }
        }

        public static Layout Read(string path)
        {
            path = Path.GetFullPath(path);
            using (var file = new FileStream(path, FileMode.Open, FileAccess.Read,
                FileShare.ReadWrite | FileShare.Delete))
            {
                // A parent junction can cross volumes. Query cluster size on the
                // opened file's volume, rather than its logical drive letter.
                uint sectors, sectorBytes, free, total;
                if (!GetDiskFreeSpace(VolumeRoot(file.SafeFileHandle), out sectors, out sectorBytes, out free, out total))
                    throw new Win32Exception(Marshal.GetLastWin32Error());
                var identity = FileIdentity(file.SafeFileHandle);
                var extents = new List<Extent>();
                long start = 0;
                for (int page = 0; ; page++)
                {
                    if (page > 10000) throw new IOException("Too many extent pages.");
                    var input = BitConverter.GetBytes(start);
                    var output = new byte[65536];
                    uint returned;
                    bool ok = DeviceIoControl(file.SafeFileHandle, 0x00090073,
                        input, 8, output, (uint)output.Length, out returned, IntPtr.Zero);
                    int error = ok ? 0 : Marshal.GetLastWin32Error();
                    if (error == 38) break; // EOF, including resident data.
                    if (!ok && error != 234) throw new Win32Exception(error);
                    if (returned < 16) throw new IOException("Truncated extent header.");
                    uint count = BitConverter.ToUInt32(output, 0);
                    if (count == 0 || count > (returned - 16) / 16)
                        throw new IOException("Invalid extent count.");
                    long vcn = BitConverter.ToInt64(output, 8);
                    for (uint i = 0; i < count; i++)
                    {
                        int offset = 16 + (int)i * 16;
                        long end = BitConverter.ToInt64(output, offset);
                        long lcn = BitConverter.ToInt64(output, offset + 8);
                        if (vcn < 0 || end <= vcn) throw new IOException("Invalid extent range.");
                        extents.Add(new Extent { Start = vcn, End = end, Lcn = lcn });
                        vcn = end;
                    }
                    if (ok) break;
                    if (vcn <= start) throw new IOException("Extent query made no progress.");
                    start = vcn;
                }
                return new Layout {
                    Volume = BitConverter.ToUInt64(identity, 0),
                    FileId = BitConverter.ToString(identity, 8, 16),
                    ClusterBytes = (long)sectors * sectorBytes,
                    Length = file.Length,
                    Extents = extents.ToArray()
                };
            }
        }

        public static long SharedBytes(Layout source, Layout target)
        {
            if (source.Volume != target.Volume) return 0;
            if (source.FileId == target.FileId)
                throw new IOException("Source and target refer to the same file or a hard link.");
            if (source.ClusterBytes != target.ClusterBytes)
                throw new IOException("Cluster sizes differ on the same volume.");
            long bytes = 0;
            int a = 0, b = 0;
            while (a < source.Extents.Length && b < target.Extents.Length)
            {
                var x = source.Extents[a];
                var y = target.Extents[b];
                long start = Math.Max(x.Start, y.Start);
                long end = Math.Min(x.End, y.End);
                if (end > start && x.Lcn >= 0 && y.Lcn >= 0 &&
                    x.Lcn + start - x.Start == y.Lcn + start - y.Start)
                    bytes += (end - start) * source.ClusterBytes;
                if (x.End <= y.End) a++;
                if (y.End <= x.End) b++;
            }
            return Math.Min(bytes, Math.Min(source.Length, target.Length));
        }
    }
}
