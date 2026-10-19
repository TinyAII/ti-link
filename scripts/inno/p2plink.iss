; TI 联机 —— 安装包脚本（Inno Setup 6）
;
; 一般不用手动跑，scripts/build-client.sh 会调它：
;   ISCC.exe /DAppVersion=1.0.1 scripts\inno\p2plink.iss
;
; 产物：dist\TI联机-安装包-<版本>.exe
;
; ────────────────────────────────────────────────────────────────
; 为什么这么设计（几条都是踩过或想清楚才定的，别随手改）
; ────────────────────────────────────────────────────────────────
;
; ① **不提权**（PrivilegesRequired=lowest，装到 %LocalAppData%\Programs）
;    没买代码签名证书的程序，SmartScreen 会拦一次「未知发布者」。
;    如果安装器还要管理员，用户会遇到 **UAC 提权 + SmartScreen 两道**，
;    而且第二道出现在"这个来路不明的程序要管理员权限"的语境里 —— 比绿色 exe 更难接受 ✗
;    装到用户目录 ⇒ 零 UAC ⇒ 体验和绿色版一样，还是只有一道 ✓
;    数据本来就在 %AppData%\p2plink（internal/appdir），跟装在哪无关 ✓
;
; ② **不建开机自启**
;    程序自己在 HKCU\...\Run\P2PLink 里管这事（settings_windows.go），
;    设置页有开关。安装器再写一条 = 两个来源打架，用户在设置里关了
;    开机还是启动，怎么查都查不出来 ✗
;    反过来，**卸载时必须把那条清掉**（见 CurUninstallStepChanged）✓
;
; ③ **卸载询问要不要删数据**
;    默认**保留** %AppData%\p2plink（账号令牌 + 设置），重装后直接还在 ✓
;    但把选择权交给用户，别替他决定 ✓
;
; ④ WebView2 只在**缺失时提醒**，不打进安装包
;    内嵌运行时会让安装包从 8MB 涨到 100MB+，而 Win10 1803+/Win11 都自带 ✓
;    （缺了的表现是**界面白屏**，用户完全看不懂 —— 所以要提前说一句）

#define AppName "TI 联机"
// 装到用户机器上的文件名（老板 2026-10-17：**中文名**，别让用户看到 p2plink-gui.exe）
#define AppExe "TI联机.exe"

// 卸载器也改中文名（Inno 没有 UninstallExeName 指令，只能在安装收尾时改名，
// 见下面 [Code] 的 RenameUninstallerFiles）
#define AppUninst "TI联机删除器.exe"
#define AppPublisher "TI"

; 版本号由构建脚本从 cmd/gui/settings.go 的 appVersion 抠出来传进来，
; **不要在这儿再写一份** —— 两处版本号迟早对不上 ✗
#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif

[Setup]
; AppId 决定了"是不是同一个软件"（覆盖升级 / 卸载识别都靠它）。
; 🩸 一旦发布出去就**不能改**，改了等于变成另一个软件，老版本卸不掉 ✗
AppId={{2C19FC2B-E7FB-4D07-B368-0D4377AF7435}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#AppPublisher}
DefaultDirName={localappdata}\Programs\TI联机
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
; 升级时沿用上次的安装目录和任务勾选
UsePreviousAppDir=yes
UsePreviousTasks=yes
; 📌 目录页的显示规则（2026-10-18 查证，别搞反了）：
;
;   UsePreviousAppDir=yes 只负责**把上次的路径预填**进去，跟"弹不弹这页"是两件事 ✓
;   DisableDirPage 的**默认值就是 auto**，所以下面这行写不写效果一样
;   （实测：加这条前后编出来的安装包**字节完全相同**，哈希一致）✗
;
;   auto 的语义：**检测到已装过 → 不显示目录页，直接装回原路径**；
;                检测不到（首次装 / 先卸载了 / 换了 Windows 用户）→ 显示，让人选。
;
;   🩸 所以"每次都要选"通常不是这条的锅，而是"每次都被判断成首次安装" ——
;     最常见的原因是**两次运行的权限不一样**：
;       双击 = 普通用户（卸载信息在 HKCU）
;       右键"以管理员身份运行" = 提升后（卸载信息在 HKLM）
;     两边互相看不见 ⇒ 每次都当首次 ✗
;
;   真要**永远不显示**就写 DisableDirPage=yes，代价是用户再也改不了安装位置。
DisableDirPage=auto
; 程序正在跑的话，让 Restart Manager 请它先退出，别覆盖半截
CloseApplications=yes
RestartApplications=no
; 保数据：配置全在 %AppData%，不在安装目录
SourceDir=..\..
OutputDir=dist
OutputBaseFilename=TI联机-安装包-{#AppVersion}
SetupIconFile=cmd\gui\app.ico
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
Compression=lzma2/ultra64
SolidCompression=yes
; modern dynamic = 跟随系统深浅色（Inno 6.3+ 才支持 dark 那一档）
WizardStyle=modern dynamic
; Inno 6 起欢迎页默认**不显示**，而左侧那张品牌大图只在欢迎页 / 完成页出现
; ⇒ 想要"像个正经软件"的第一印象，就把欢迎页打开（代价是多一次「下一步」）
DisableWelcomePage=no
WizardResizable=no
; 品牌图：每档 DPI 各一张，Inno 按系统缩放自动挑（图片由 mkimages.py 生成）
WizardImageFile=scripts\inno\img\wizard-100.png,scripts\inno\img\wizard-125.png,scripts\inno\img\wizard-150.png,scripts\inno\img\wizard-175.png,scripts\inno\img\wizard-200.png
WizardSmallImageFile=scripts\inno\img\small-100.png,scripts\inno\img\small-125.png,scripts\inno\img\small-150.png,scripts\inno\img\small-175.png,scripts\inno\img\small-200.png
VersionInfoVersion={#AppVersion}
VersionInfoProductName={#AppName}
VersionInfoDescription={#AppName} 安装程序

[Languages]
; 简体中文语言文件是从 Inno 官方仓库取的，放在 scripts\inno\ 一起进 git
; （Inno Setup 6 自带的那 30 种语言里**没有中文** ✗）
Name: "zh"; MessagesFile: "scripts\inno\ChineseSimplified.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务："

[Files]
; 📌 不再带 使用说明.txt（老板 2026-10-17）——isreadme 那个「查看说明」勾选也就没了 ✓
;    文档内容在仓库的 docs/使用说明.txt，客户端里有「帮助」页、官网也有
Source: "dist\TI联机\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion
; 📌 命令行版不再随包发（没有登录功能，玩家跑不起来）。编译产物在 dist/p2plink.exe，
;    自己调试时直接用，不进安装包 ✓

[InstallDelete]
; 🩸 覆盖升级时**必须**删掉老版本的文件名 ——
;    旧版装的是 p2plink-gui.exe / p2plink.exe / 使用说明.txt，
;    改名之后它们会**留在原地**，用户目录里就变成两套 exe ✗
;    而且开机自启那条注册表（HKCU\...\Run\P2PLink）还指着旧路径 ⇒ 指向不存在的文件 ✗
Type: files; Name: "{app}\p2plink-gui.exe"
Type: files; Name: "{app}\p2plink.exe"
Type: files; Name: "{app}\使用说明.txt"
; 上一版把命令行版也装进去了 ⇒ 升级时要一并清掉
Type: files; Name: "{app}\TI联机-命令行.exe"

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\{#AppExe}"
; 卸载项直接指向**改名后**的卸载器（它由下面的 [Code] 在安装收尾时生成）✓
Name: "{group}\卸载 {#AppName}"; Filename: "{app}\{#AppUninst}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#AppExe}"; Description: "立即启动 {#AppName}"; Flags: nowait postinstall skipifsilent

[Code]

{ WebView2 运行时在不在。
  ⚠️ 三个位置都要查：按机器装（32/64 位视图各一个）+ 按用户装。
  漏掉 HKCU 那个的话，"只给当前用户装过 WebView2"的机器会被误判成缺失 ✗ }
const
  WebView2Client = '{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}';

function HasWebView2(): Boolean;
var
  v: String;
begin
  Result :=
    RegQueryStringValue(HKLM32, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\' + WebView2Client, 'pv', v) or
    RegQueryStringValue(HKLM64, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\' + WebView2Client, 'pv', v) or
    RegQueryStringValue(HKCU,   'Software\Microsoft\EdgeUpdate\Clients\' + WebView2Client, 'pv', v);
end;

function InitializeSetup(): Boolean;
var
  err: Integer;
begin
  Result := True;

  { 静默安装（/VERYSILENT）绝对不能弹窗 —— 会一直卡在那儿等人点，
    而调用方在等进程退出 ⇒ 死锁 ✗ }
  if WizardSilent then
    Exit;

  if HasWebView2() then
    Exit;

  { 只是提醒，不拦着装 —— 用户可能想先装完再去补运行时。
    但必须说清"装完打开会白屏"，否则他会以为软件坏了 ✗ }
  if MsgBox('这台电脑好像没有 WebView2 运行时。' + #13#10 + #13#10 +
            'TI 联机的界面是靠它渲染的，缺了会白屏（窗口打开但一片空白）。' + #13#10 + #13#10 +
            'Win10 1803 以后的版本、Win11 都自带，一般不用管。' + #13#10 +
            '如果你的系统比较老（Win7 / Win8 / 精简版），建议先装运行时。' + #13#10 + #13#10 +
            '现在打开微软的下载页吗？（安装会继续）',
            mbConfirmation, MB_YESNO or MB_DEFBUTTON1) = IDYES then
  begin
    ShellExec('open', 'https://go.microsoft.com/fwlink/p/?LinkId=2124703',
              '', '', SW_SHOWNORMAL, ewNoWait, err);
  end;
end;

// 数据目录可能在**别的地方** —— 设置页能改，指针在 HKCU\Software\P2PLink\DataParent。
//
// 🩸 原来这里写死 %AppData%\p2plink：用户改过数据目录的话，
//    弹窗显示的路径是错的，选「是」删的也是那个空目录 ⇒ **真数据原封不动留在原处** ✗
//    （2026-10-18 查出来的）
//
// 📌 注释只能用 // 或 (* *)：{} 里写 {userappdata} 会把注释提前截断，编译直接报
//    "Syntax error"（这次就是这么栽的）✗
function RealDataDir(): String;
var
  parent: String;
begin
  Result := ExpandConstant('{userappdata}\p2plink');
  if RegQueryStringValue(HKCU, 'Software\P2PLink', 'DataParent', parent) and (parent <> '') then
    Result := AddBackslash(parent) + 'p2plink';
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  dataDir: String;
begin
  if CurUninstallStep = usUninstall then
  begin
    { 🩸 开机自启是程序自己写进 HKCU\Run 的（值名 P2PLink）。
      不清掉的话，卸载后每次开机都会有一条指向已删除文件的启动项 ——
      用户看不到任何东西，只有系统日志里留一行错 ✗ }
    RegDeleteValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run', 'P2PLink');
  end;

  if CurUninstallStep = usPostUninstall then
  begin
    if UninstallSilent then
      Exit;                       { 静默卸载不许弹窗，默认保留数据 }

    dataDir := RealDataDir();
    // 写进卸载日志（只在带 /LOG 运行时产生），排查"卸载删了哪儿"用得上
    Log('卸载：算出的数据目录 = ' + dataDir);
    if not DirExists(dataDir) then
    begin
      { 没有数据目录，但指针还在 ⇒ 一并清掉。
        留着的话下次装会继续指向一个空地方，用户会莫名其妙 ✗ }
      RegDeleteKeyIncludingSubkeys(HKCU, 'Software\P2PLink');
      Exit;
    end;

    { 默认按钮放在「否」：手快点错不该把账号和设置一起扬了 ✗ }
    if MsgBox('要顺便删掉账号和设置吗？' + #13#10 + #13#10 +
              dataDir + #13#10 + #13#10 +
              '选「是」：一起删除。下次装要重新登录（邀请码也得重填）。' + #13#10 +
              '选「否」：保留。以后重装，账号和设置都还在。',
              mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
    begin
      Log('卸载：用户选了「是」，开始删除 ' + dataDir);
      DelTree(dataDir, True, True, True);
      // 📌 Inno 的脚本里**没有** BoolToStr（编译会报 Unknown identifier）⇒ 用 if 分开写 ✓
      if DirExists(dataDir) then
        Log('卸载：删除后目录还在（可能被占用）')
      else
        Log('卸载：数据目录已删除');
      { 数据没了，指针也没意义。
        📌 只在**确实删了数据**时清：选了「否」还清指针的话，
        数据就变成没人认领的孤儿，下次装也找不回来 ✗ }
      RegDeleteKeyIncludingSubkeys(HKCU, 'Software\P2PLink');
    end;
  end;
end;

// ==================== 卸载器改名 ====================
//
// 🩸 Inno 6 **没有** UninstallExeName 这个指令 —— 我试过，编译器直接报
//   `Unrecognized [Setup] section directive "UninstallExeName"` ✗
//   ⇒ 只能等它把 unins000.exe 装完之后再改名。
//
// 三个细节，少一个就废：
//  ① 卸载器是**按自己的文件名**去找同名 .dat 的 ⇒ exe 和 dat 必须一起改 ✓
//  ② 注册表里 UninstallString / QuietUninstallString 还写着旧路径 ⇒
//     「应用和功能」里点卸载会找不到文件 ⇒ 跟着改 ✓
//  ③ 必须放在 **ssDone**（所有步骤都完、注册表也写完了）再改 ——
//     放 ssPostInstall 的话，Inno 之后还会把 uninstall 信息重写一遍，
//     我们的修改就被覆盖回去了 ✗
//
// 📌 注释别写成 /* */ 那种 C 风格：Inno 的 Pascal 不认，编译直接报
//    "'BEGIN' expected"。也别写成 {} —— 里面带 {app} 这种大括号会被提前截断 ✗
procedure RenameUninstallerFiles();
var
  app, oldExe, oldDat, newExe, newDat, key: String;
begin
  app := ExpandConstant('{app}');
  oldExe := app + '\unins000.exe';
  oldDat := app + '\unins000.dat';
  newExe := app + '\{#AppUninst}';
  newDat := app + '\TI联机删除器.dat';

  if not FileExists(oldExe) then
    Exit;
  DeleteFile(newExe);
  DeleteFile(newDat);
  if not RenameFile(oldExe, newExe) then
    Exit;
  RenameFile(oldDat, newDat);

  { 这个 GUID 跟上面 [Setup] 里的 AppId 是同一个 —— 改一处要改两处 }
  key := 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{2C19FC2B-E7FB-4D07-B368-0D4377AF7435}_is1';
  RegWriteStringValue(HKCU, key, 'UninstallString', '"' + newExe + '"');
  RegWriteStringValue(HKCU, key, 'QuietUninstallString', '"' + newExe + '" /SILENT');
end;

// 升级时把开机自启那条改到新文件名上。
//
// 🩸 老版本装的是 p2plink-gui.exe，而且客户端自己会往
//    HKCU\...\Run\P2PLink 写"带引号的完整路径"。
//    改名之后：新的 [InstallDelete] 会把旧 exe 删掉，可那条注册表还指着它
//    ⇒ 每次开机系统都去启一个不存在的文件 ✗（用户看不到任何提示）
//    ⇒ 只在**指向本程序目录**时才改，别的软件写的同名值一律不动 ✓
procedure FixAutoStartPath();
var
  cur, appDir, newExe: String;
begin
  if not RegQueryStringValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run',
                             'P2PLink', cur) then
    Exit;
  appDir := ExpandConstant('{app}');
  if Pos(LowerCase(appDir), LowerCase(cur)) = 0 then
    Exit;
  newExe := appDir + '\{#AppExe}';
  RegWriteStringValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run',
                      'P2PLink', '"' + newExe + '"');
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssDone then
  begin
    RenameUninstallerFiles();
    FixAutoStartPath();
  end;
end;
