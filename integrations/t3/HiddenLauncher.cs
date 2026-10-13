// Optional Windows GUI entry point for a PowerShell COW launcher.
// Build with the Windows .NET Framework compiler using /target:winexe.
using System;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Windows.Forms;

internal static class HiddenLauncher
{
    // ProcessStartInfo.Arguments uses native Windows argv quoting. Do not run
    // a shell command or interpolate application arguments into PowerShell code.
    private static string Quote(string value)
    {
        StringBuilder result = new StringBuilder("\"");
        int slashes = 0;
        foreach (char character in value)
        {
            if (character == '\\')
            {
                slashes++;
                continue;
            }
            result.Append('\\', character == '"' ? slashes * 2 + 1 : slashes);
            result.Append(character);
            slashes = 0;
        }
        result.Append('\\', slashes * 2);
        return result.Append('"').ToString();
    }

    [STAThread]
    private static int Main(string[] args)
    {
        try
        {
            if (args.Length == 0 || !Path.IsPathRooted(args[0]))
                throw new ArgumentException("The first argument must be an absolute PowerShell launcher path.");
            string script = Path.GetFullPath(args[0]);
            if (!File.Exists(script) || !String.Equals(Path.GetExtension(script), ".ps1", StringComparison.OrdinalIgnoreCase))
                throw new ArgumentException("PowerShell launcher not found: " + script);

            string powershell = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System),
                @"WindowsPowerShell\v1.0\powershell.exe");
            StringBuilder arguments = new StringBuilder("-NoLogo -NoProfile -NonInteractive -STA -ExecutionPolicy Bypass -File ");
            arguments.Append(Quote(script));
            for (int i = 1; i < args.Length; i++)
                arguments.Append(' ').Append(Quote(args[i]));

            // This executable has the GUI subsystem, and CREATE_NO_WINDOW keeps
            // PowerShell from creating a console even briefly during startup.
            ProcessStartInfo start = new ProcessStartInfo(powershell, arguments.ToString());
            start.UseShellExecute = false;
            start.CreateNoWindow = true;
            using (Process child = Process.Start(start))
            {
                child.WaitForExit();
                return child.ExitCode;
            }
        }
        catch (Exception error)
        {
            MessageBox.Show(error.Message, "COW launcher", MessageBoxButtons.OK, MessageBoxIcon.Warning);
            return 1;
        }
    }
}
