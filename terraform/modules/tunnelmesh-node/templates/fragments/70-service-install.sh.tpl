# Start the tunnelmesh service
%{ if coordinator_enabled ~}
/usr/local/bin/tunnelmesh service start --name tunnelmesh-server
%{ else ~}
/usr/local/bin/tunnelmesh service start --name tunnelmesh
%{ endif ~}
