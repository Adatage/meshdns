package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/Adatage/meshdns/pkg/proto"
)

var (
	grpcAddr string
	rootCmd  = &cobra.Command{
		Use:   "dnsctl",
		Short: "CLI for managing the DNS server via gRPC",
	}
)

func main() {
	rootCmd.PersistentFlags().StringVar(&grpcAddr, "addr", envStr("DNS_GRPC_ADDR", "localhost:50051"), "gRPC server address (env: DNS_GRPC_ADDR)")

	rootCmd.AddCommand(
		statusCmd(),
		zoneCmd(),
		recordCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func dial() (*grpc.ClientConn, pb.DNSControlClient, error) {
	conn, err := grpc.NewClient(grpcAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to %s: %w", grpcAddr, err)
	}
	return conn, pb.NewDNSControlClient(conn), nil
}

func withClient(fn func(pb.DNSControlClient) error) error {
	conn, client, err := dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(client)
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show server status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(func(c pb.DNSControlClient) error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				resp, err := c.GetStatus(ctx, &pb.GetStatusRequest{})
				if err != nil {
					return err
				}

				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintf(w, "Version:\t%s\n", resp.Version)
				fmt.Fprintf(w, "Uptime:\t%s\n", (time.Duration(resp.UptimeSeconds)*time.Second).String())
				fmt.Fprintf(w, "Recursive:\t%v\n", resp.RecursiveEnabled)
				fmt.Fprintf(w, "Authoritative:\t%v\n", resp.AuthoritativeEnabled)
				fmt.Fprintf(w, "Cache:\t%v\n", resp.CacheEnabled)
				fmt.Fprintf(w, "UDP Bind:\t%s\n", orDash(resp.BindUdp))
				fmt.Fprintf(w, "TCP Bind:\t%s\n", orDash(resp.BindTcp))
				fmt.Fprintf(w, "gRPC Addr:\t%s\n", resp.GrpcAddr)
				return w.Flush()
			})
		},
	}
}

func zoneCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "zone", Short: "Manage authoritative zones"}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all zones",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(func(c pb.DNSControlClient) error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				resp, err := c.ListZones(ctx, &pb.ListZonesRequest{})
				if err != nil {
					return err
				}
				if len(resp.Zones) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "no zones configured")
					return nil
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tNAME\tCREATED")
				for _, z := range resp.Zones {
					fmt.Fprintf(w, "%s\t%s\t%s\n", z.Id, z.Name, z.CreatedAt)
				}
				return w.Flush()
			})
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "create <name>",
		Short: "Create a new authoritative zone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(c pb.DNSControlClient) error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				resp, err := c.CreateZone(ctx, &pb.CreateZoneRequest{Name: args[0]})
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created zone %s (id=%s)\n", resp.Zone.Name, resp.Zone.Id)
				return nil
			})
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a zone and all its records",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(c pb.DNSControlClient) error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				_, err := c.DeleteZone(ctx, &pb.DeleteZoneRequest{Name: args[0]})
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "deleted zone %s\n", args[0])
				return nil
			})
		},
	})

	return cmd
}

func recordCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "record", Short: "Manage DNS records"}

	cmd.AddCommand(&cobra.Command{
		Use:   "list <zone>",
		Short: "List records in a zone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(c pb.DNSControlClient) error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				resp, err := c.ListRecords(ctx, &pb.ListRecordsRequest{ZoneName: args[0]})
				if err != nil {
					return err
				}
				if len(resp.Records) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "no records")
					return nil
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tNAME\tTYPE\tTTL\tDATA\tSUBNET")
				for _, r := range resp.Records {
					fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", r.Id, r.Name, r.Type, r.Ttl, r.Data, orDash(r.Subnet))
				}
				return w.Flush()
			})
		},
	})

	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Add a record to a zone",
		RunE: func(cmd *cobra.Command, _ []string) error {
			zone, _ := cmd.Flags().GetString("zone")
			name, _ := cmd.Flags().GetString("name")
			rtype, _ := cmd.Flags().GetString("type")
			ttl, _ := cmd.Flags().GetUint32("ttl")
			data, _ := cmd.Flags().GetString("data")
			subnet, _ := cmd.Flags().GetString("subnet")

			if zone == "" || name == "" || rtype == "" || data == "" {
				return fmt.Errorf("--zone, --name, --type and --data are required")
			}

			return withClient(func(c pb.DNSControlClient) error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				resp, err := c.AddRecord(ctx, &pb.AddRecordRequest{
					ZoneName: zone,
					Name:     name,
					Type:     strings.ToUpper(rtype),
					Ttl:      ttl,
					Data:     data,
					Subnet:   subnet,
				})
				if err != nil {
					return err
				}
				subnetInfo := ""
				if resp.Record.Subnet != "" {
					subnetInfo = " subnet=" + resp.Record.Subnet
				}
				fmt.Fprintf(cmd.OutOrStdout(), "added record %s %s %d %s%s (id=%s)\n",
					resp.Record.Name, resp.Record.Type, resp.Record.Ttl, resp.Record.Data, subnetInfo, resp.Record.Id)
				return nil
			})
		},
	}
	addCmd.Flags().String("zone", "", "Zone name")
	addCmd.Flags().String("name", "", "Record name (FQDN)")
	addCmd.Flags().String("type", "", "Record type (A, AAAA, MX, CNAME, TXT, NS, SRV …)")
	addCmd.Flags().Uint32("ttl", 300, "TTL in seconds")
	addCmd.Flags().String("data", "", "Record data (rdata)")
	addCmd.Flags().String("subnet", "", "Optional CIDR subnet (e.g. 10.0.0.0/8) — only clients from this subnet receive this record")
	cmd.AddCommand(addCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a record by UUID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(c pb.DNSControlClient) error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				_, err := c.DeleteRecord(ctx, &pb.DeleteRecordRequest{Id: args[0]})
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "deleted record %s\n", args[0])
				return nil
			})
		},
	})

	return cmd
}

func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

var _ = strconv.Itoa
